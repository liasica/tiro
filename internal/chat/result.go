package chat

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/liasica/tiro/internal/llm"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/summary"
)

//go:embed result.md
var resultPrompt string

// ResultKind 是回答整理成文档时的结果类型
type ResultKind struct {
	Key   string
	Label string
}

// ResultKinds 是全部结果类型，模型无法判断时按最后一种处理
var ResultKinds = []ResultKind{
	{Key: "todo", Label: "待办清单"},
	{Key: "summary", Label: "会议总结"},
	{Key: "decision", Label: "决策记录"},
	{Key: "progress", Label: "进展追踪"},
	{Key: "risk", Label: "风险问题"},
	{Key: "brief", Label: "专题整理"},
}

// 模型判断不值得保存时写的类型
const skipLabel = "不保存"

var (
	// resultHeader 匹配模型输出开头的「类型：」「标题：」两行
	resultHeader = regexp.MustCompile(`^\*{0,2}(类型|标题)\*{0,2}\s*[:：]\s*(.*)$`)

	// xmlBlockOpen 与 xmlBlockClose 匹配正文里高亮块、分栏、表格的起止标签，标签内的链接要写成 XML
	xmlBlockOpen  = regexp.MustCompile(`<(callout|grid|column|table)[\s>]`)
	xmlBlockClose = regexp.MustCompile(`</(callout|grid|column|table)>`)
)

// ResultInput 是把一条回答整理成文档所需的材料
type ResultInput struct {
	Question string
	AskedAt  time.Time
	Asker    string
	// Scope 是提问时的检索范围说明
	Scope  string
	Answer *store.ChatMessage
	// Topics 是妙记记录 ID 到纪要会议主题的对应关系，妙记标题常是录音设备的默认名称，有主题时优先用主题
	Topics map[int64]string
}

// sourceName 返回引用的会议名称，有纪要主题时用主题
func (input ResultInput) sourceName(source store.ChatSource) string {
	if topic := strings.TrimSpace(input.Topics[source.MinuteID]); topic != "" {
		return topic
	}

	return source.Title
}

// ResultDoc 是整理好的结果文档，Kind 为空表示回答不值得保存
type ResultDoc struct {
	Kind     string
	Title    string
	Markdown string
}

// ComposeResult 让模型判断回答的结果类型，并按该类型的版式整理成飞书文档
//
// 出错时也返回本次调用的用量
func ComposeResult(ctx context.Context, model llm.Config, input ResultInput) (*ResultDoc, llm.Usage, error) {
	session := llm.NewSession(model, strings.TrimSpace(resultPrompt), nil)

	reply, usage, err := session.Send(ctx, []llm.Message{{Role: llm.RoleUser, Content: resultMaterial(input)}}, llm.Turn{}, nil)
	if err != nil {
		return nil, usage, err
	}

	kind, subject, body := parseResult(reply.Content)
	if kind == nil {
		return &ResultDoc{}, usage, nil
	}

	if strings.TrimSpace(body) == "" {
		return nil, usage, errors.New("模型没有给出文档正文")
	}

	if subject == "" {
		subject = TitleFrom(input.Question)
	}

	doc := &ResultDoc{
		Kind:     kind.Key,
		Title:    kind.Label + "：" + subject,
		Markdown: renderResult(input, body, usage),
	}

	return doc, usage, nil
}

// ResultLabel 返回结果类型的名称，未知类型返回空串
func ResultLabel(key string) string {
	for _, kind := range ResultKinds {
		if kind.Key == key {
			return kind.Label
		}
	}

	return ""
}

// resultMaterial 把提问、引用的会议与回答拼成给模型的输入，回答里的引用换回以引用序号为编号的出处标记
func resultMaterial(input ResultInput) string {
	var material strings.Builder

	material.WriteString("# 提问\n\n")
	material.WriteString(strings.TrimSpace(input.Question))
	material.WriteString("\n\n提问时间：" + formatMoment(input.AskedAt.Local()) + "\n")

	material.WriteString("\n# 引用的会议\n\n")

	for _, source := range input.Answer.Sources {
		line := "- [#" + strconv.Itoa(source.Index) + "] " + input.sourceName(source)

		if moment, ok := sourceTime(source); ok {
			line += "，" + formatMoment(moment)
		}

		if source.DurationSeconds > 0 {
			line += "，时长 " + shortDuration(source.DurationSeconds)
		}

		material.WriteString(line + "\n")
	}

	answer := ReplaceCitations(input.Answer.Content, func(index int, moment string) string {
		marker := "[#" + strconv.Itoa(index)

		if moment != "" {
			marker += "@" + moment
		}

		return marker + "]"
	})

	material.WriteString("\n# 回答\n\n" + strings.TrimSpace(answer) + "\n")

	return material.String()
}

// parseResult 取出模型输出的类型、标题与正文；类型为「不保存」时返回 nil，认不出的类型按专题整理处理
func parseResult(reply string) (*ResultKind, string, string) {
	lines := strings.Split(stripFence(reply), "\n")
	label, subject := "", ""

	// 类型与标题只会出现在开头，遇到其他内容就是正文
	for len(lines) > 0 {
		line := strings.TrimSpace(lines[0])
		if line == "" {
			lines = lines[1:]

			continue
		}

		match := resultHeader.FindStringSubmatch(line)
		if match == nil {
			break
		}

		value := strings.TrimSpace(strings.Trim(match[2], "*"))
		if match[1] == "类型" {
			label = value
		} else {
			subject = value
		}

		lines = lines[1:]
	}

	if strings.Contains(label, skipLabel) {
		return nil, "", ""
	}

	kind := &ResultKinds[len(ResultKinds)-1]

	for i := range ResultKinds {
		if strings.Contains(label, ResultKinds[i].Label) {
			kind = &ResultKinds[i]

			break
		}
	}

	return kind, subject, strings.TrimSpace(strings.Join(lines, "\n"))
}

// stripFence 去掉模型把整篇输出包进代码块时的首尾围栏
func stripFence(reply string) string {
	text := strings.TrimSpace(reply)
	if !strings.HasPrefix(text, "```") {
		return text
	}

	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return text
	}

	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

// renderResult 拼出完整的文档正文：头部信息、模型整理的正文、引用来源与模型消耗
func renderResult(input ResultInput, body string, usage llm.Usage) string {
	var doc strings.Builder

	doc.WriteString(resultHeaderBlock(input))
	doc.WriteString("\n")
	doc.WriteString(linkResultCitations(body, input.Answer.Sources))
	doc.WriteString("\n")

	if len(input.Answer.Sources) > 0 {
		doc.WriteString("\n# 引用来源\n\n")
		doc.WriteString(sourceTable(input))
	}

	doc.WriteString(resultUsage(input.Answer, usage))

	return doc.String()
}

// resultHeaderBlock 写出文档开头的引用块：问题、涉及的会议、检索范围与提问人
func resultHeaderBlock(input ResultInput) string {
	lines := []string{"问题：" + summary.EscapeMarkdown(strings.Join(strings.Fields(input.Question), " "))}

	if meetings := meetingSpan(input.Answer.Sources); meetings != "" {
		lines = append(lines, "涉及会议："+meetings)
	}

	if input.Scope != "" {
		lines = append(lines, "检索范围："+summary.EscapeMarkdown(input.Scope))
	}

	asked := longMoment(input.AskedAt.Local())
	if input.Asker != "" {
		asked = summary.EscapeMarkdown(input.Asker) + "，" + asked
	}

	lines = append(lines, "提问人："+asked)

	return "> " + strings.Join(lines, "\n> ") + "\n"
}

// meetingSpan 写出引用了几场会议以及它们的日期范围，例如「3 场，2026年11月10日 至 11月12日」
func meetingSpan(sources []store.ChatSource) string {
	if len(sources) == 0 {
		return ""
	}

	var first, last time.Time

	for _, source := range sources {
		moment, ok := sourceTime(source)
		if !ok {
			continue
		}

		if first.IsZero() || moment.Before(first) {
			first = moment
		}

		if last.IsZero() || moment.After(last) {
			last = moment
		}
	}

	text := strconv.Itoa(len(sources)) + " 场"

	switch {
	case first.IsZero():
		return text
	case sameDay(first, last):
		return text + "，" + first.Format("2006年1月2日")
	case first.Year() == last.Year():
		return text + "，" + first.Format("2006年1月2日") + " 至 " + last.Format("1月2日")
	default:
		return text + "，" + first.Format("2006年1月2日") + " 至 " + last.Format("2006年1月2日")
	}
}

// linkResultCitations 把正文里的出处标记换成跳到纪要或妙记时间点的链接，编号不存在的标记直接删掉
//
// 高亮块、分栏与表格里的内容按 XML 解析，链接要写成 a 标签；其余地方写成 Markdown 链接，后面紧跟的空格换成不换行空格，普通空格会被飞书文档吞掉
func linkResultCitations(body string, sources []store.ChatSource) string {
	bySource := make(map[int]store.ChatSource, len(sources))
	for _, source := range sources {
		bySource[source.Index] = source
	}

	lines := strings.Split(body, "\n")
	depth := 0

	for i, line := range lines {
		inXML := depth > 0 || xmlBlockOpen.MatchString(line)

		depth = max(depth+len(xmlBlockOpen.FindAllString(line, -1))-len(xmlBlockClose.FindAllString(line, -1)), 0)

		lines[i] = linkLine(line, bySource, inXML)
	}

	return strings.Join(lines, "\n")
}

// linkLine 改写一行里的出处标记
func linkLine(line string, sources map[int]store.ChatSource, inXML bool) string {
	matches := citeGroup.FindAllStringIndex(line, -1)
	if len(matches) == 0 {
		return line
	}

	var out strings.Builder

	last := 0

	for _, match := range matches {
		out.WriteString(line[last:match[0]])

		links := citationLinks(line[match[0]:match[1]], sources, inXML)
		out.WriteString(links)

		last = match[1]

		if links != "" && !inXML && last < len(line) && line[last] == ' ' {
			out.WriteString(" ")

			last++
		}
	}

	out.WriteString(line[last:])

	return out.String()
}

// citationLinks 把一个出处标记（可能是多项合写）换成链接，同一标记里重复的项只写一次
func citationLinks(group string, sources map[int]store.ChatSource, inXML bool) string {
	var links strings.Builder

	written := make(map[string]bool)

	for _, item := range citeItem.FindAllStringSubmatch(group, -1) {
		index, err := strconv.Atoi(item[1])
		if err != nil {
			continue
		}

		source, ok := sources[index]
		if !ok {
			continue
		}

		label := normalizeMoment(item[2])
		text := "〔" + strconv.Itoa(index) + "〕"
		target := source.DocURL

		if label != "" {
			text = "〔" + strconv.Itoa(index) + " · " + label + "〕"
			target = summary.TimestampURL(source.MinuteURL, label)
		}

		if target == "" {
			target = source.MinuteURL
		}

		if written[text] || target == "" {
			continue
		}

		written[text] = true

		links.WriteString(linkMarkup(text, target, inXML))
	}

	return links.String()
}

// linkMarkup 按所处位置写出链接
func linkMarkup(text, target string, inXML bool) string {
	if inXML {
		return `<a href="` + html.EscapeString(target) + `">` + html.EscapeString(text) + `</a>`
	}

	return "[" + text + "](" + target + ")"
}

// sourceTable 用表格列出引用到的会议，引用里有别人的妙记时多一列所有者
func sourceTable(input ResultInput) string {
	sources := input.Answer.Sources
	showOwner := false

	for _, source := range sources {
		if source.Owner != "" && source.Owner != input.Asker {
			showOwner = true
		}
	}

	headers := []string{"序号", "会议", "时间", "时长", "查看"}
	widths := []int{64, 300, 170, 96, 190}

	if showOwner {
		headers = []string{"序号", "会议", "所有者", "时间", "时长", "查看"}
		widths = []int{64, 220, 96, 160, 90, 190}
	}

	var table strings.Builder

	table.WriteString("<table>\n<colgroup>")

	for _, width := range widths {
		_, _ = fmt.Fprintf(&table, `<col width="%d"/>`, width)
	}

	table.WriteString("</colgroup>\n<thead><tr>")

	for _, header := range headers {
		table.WriteString(`<th background-color="light-gray"><p>` + header + `</p></th>`)
	}

	table.WriteString("</tr></thead>\n<tbody>\n")

	for _, source := range sources {
		cells := []string{strconv.Itoa(source.Index), html.EscapeString(input.sourceName(source))}

		if showOwner {
			cells = append(cells, html.EscapeString(orDash(source.Owner)))
		}

		when := "—"

		if moment, ok := sourceTime(source); ok {
			when = moment.Format("2006-01-02 15:04")
		}

		duration := "—"

		if source.DurationSeconds > 0 {
			duration = shortDuration(source.DurationSeconds)
		}

		cells = append(cells, when, duration, sourceLinks(source))

		table.WriteString("<tr>")

		for _, cell := range cells {
			table.WriteString("<td><p>" + cell + "</p></td>")
		}

		table.WriteString("</tr>\n")
	}

	table.WriteString("</tbody>\n</table>\n")

	return table.String()
}

// sourceLinks 写出一条引用的纪要、妙记与时间点链接
func sourceLinks(source store.ChatSource) string {
	var links []string

	if source.DocURL != "" {
		links = append(links, linkMarkup("会议纪要", source.DocURL, true))
	}

	if source.MinuteURL != "" {
		links = append(links, linkMarkup("妙记", source.MinuteURL, true))
	}

	for _, moment := range source.Moments {
		links = append(links, linkMarkup(moment.Label, moment.URL, true))
	}

	if len(links) == 0 {
		return "—"
	}

	return strings.Join(links, " · ")
}

// resultUsage 写出文档末尾的模型消耗一节：回答与整理文档各用了多少 Token
func resultUsage(answer *store.ChatMessage, usage llm.Usage) string {
	var section strings.Builder

	section.WriteString("\n# 模型消耗\n\n")

	var models []string

	for _, model := range []string{answer.Model, usage.Model} {
		if model != "" && !slices.Contains(models, model) {
			models = append(models, model)
		}
	}

	if len(models) > 0 {
		section.WriteString("- 模型：" + summary.EscapeMarkdown(strings.Join(models, "、")) + "\n")
	}

	if answer.TotalTokens > 0 {
		section.WriteString("- 检索与回答：" + summary.GroupDigits(answer.TotalTokens) + " Token\n")
	}

	if usage.TotalTokens > 0 {
		section.WriteString("- 整理成文档：" + summary.GroupDigits(usage.TotalTokens) + " Token\n")
	}

	if answer.TotalTokens > 0 && usage.TotalTokens > 0 {
		section.WriteString("- 合计：" + summary.GroupDigits(answer.TotalTokens+usage.TotalTokens) + " Token\n")
	}

	return section.String()
}

// sourceTime 取出引用的妙记时间，换成本地时间
func sourceTime(source store.ChatSource) (time.Time, bool) {
	moment, err := time.Parse(time.RFC3339, source.MinuteCreatedAt)
	if err != nil {
		return time.Time{}, false
	}

	return moment.Local(), true
}

// longMoment 写出「2026年11月24日（周二）08:30」形式的时间
func longMoment(moment time.Time) string {
	return moment.Format("2006年1月2日") + "（" + weekdays[moment.Weekday()] + "）" + moment.Format("15:04")
}

// shortDuration 把秒数写成「1 小时 5 分钟」「45 分钟」「30 秒」，按分钟四舍五入
func shortDuration(seconds int64) string {
	if seconds < 60 {
		return strconv.FormatInt(seconds, 10) + " 秒"
	}

	minutes := (seconds + 30) / 60
	if minutes < 60 {
		return strconv.FormatInt(minutes, 10) + " 分钟"
	}

	text := strconv.FormatInt(minutes/60, 10) + " 小时"
	if minutes%60 > 0 {
		text += " " + strconv.FormatInt(minutes%60, 10) + " 分钟"
	}

	return text
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func orDash(text string) string {
	if strings.TrimSpace(text) == "" {
		return "—"
	}

	return text
}
