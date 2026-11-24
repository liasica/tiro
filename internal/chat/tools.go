package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/liasica/tiro/internal/llm"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/summary"
)

// 检索步骤的类别
const (
	StepSearch     = "search"
	StepRead       = "read"
	StepTranscript = "transcript"
)

const (
	toolSearch     = "search_minutes"
	toolRead       = "read_minutes"
	toolTranscript = "read_transcript"

	dateLayout = "2006-01-02"

	defaultSearchLimit = 30
	maxSearchLimit     = 50

	// 一次最多读几篇纪要
	maxReadMinutes = 8

	// 逐字稿每次最多返回的字数
	transcriptChunkRunes = 30000
)

var weekdays = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

var (
	// summaryTail 匹配纪要末尾由程序填写的两节，对回答没有用处
	summaryTail = regexp.MustCompile(`(?m)^# (相关链接|模型消耗)\s*$`)

	// markdownLink 匹配 Markdown 链接，只保留文字
	markdownLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

	// xmlTag 匹配纪要里飞书文档的 XML 标签
	xmlTag = regexp.MustCompile(`</?[a-zA-Z][\w-]*(?:\s[^<>]*)?/?>`)

	// escaped 匹配纪要里被转义的 Markdown 字符
	escaped = regexp.MustCompile("\\\\([\\\\`*_\\[\\]$~<])")

	blankLines = regexp.MustCompile(`\n{3,}`)
)

// tools 是提供给模型的检索工具
var tools = []llm.Tool{
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        toolSearch,
			Description: "按时间段与关键词检索妙记，按时间倒序返回妙记编号、时间、时长、标题与纪要状态。时间段与关键词都可以不填",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"start_date": map[string]string{"type": "string", "description": "开始日期，YYYY-MM-DD，含当天"},
					"end_date":   map[string]string{"type": "string", "description": "结束日期，YYYY-MM-DD，含当天"},
					"keyword":    map[string]string{"type": "string", "description": "匹配标题与纪要正文的关键词，只填一个词"},
					"limit":      map[string]any{"type": "integer", "description": "最多返回几条，默认 30，最大 50"},
				},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        toolRead,
			Description: "按编号阅读妙记的会议纪要全文，包括总结、关键决策、待办、风险、待确认与会议章节，一次最多 8 条",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ids": map[string]any{
						"type":        "array",
						"items":       map[string]string{"type": "integer"},
						"description": "妙记编号",
					},
				},
				"required": []string{"ids"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        toolTranscript,
			Description: "按编号阅读妙记的逐字稿，每段开头是说话人与时间，单次最多返回 30000 字，更长时用 offset 继续读",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]string{"type": "integer", "description": "妙记编号"},
					"offset": map[string]string{"type": "integer", "description": "从第几个字开始读，默认 0"},
				},
				"required": []string{"id"},
			},
		},
	},
}

// callTool 执行一次工具调用，结果以文本交回模型，出错时也以文本说明
func (r *run) callTool(ctx context.Context, call llm.ToolCall) string {
	arguments := []byte(strings.TrimSpace(call.Function.Arguments))
	if len(arguments) == 0 {
		arguments = []byte("{}")
	}

	switch call.Function.Name {
	case toolSearch:
		return r.searchMinutes(ctx, arguments)
	case toolRead:
		return r.readMinutes(ctx, arguments)
	case toolTranscript:
		return r.readTranscript(ctx, arguments)
	default:
		return "没有这个工具：" + call.Function.Name
	}
}

// searchMinutes 在检索范围内按时间段与关键词找妙记
func (r *run) searchMinutes(ctx context.Context, arguments []byte) string {
	var input struct {
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Keyword   string `json:"keyword"`
		Limit     int    `json:"limit"`
	}

	if err := json.Unmarshal(arguments, &input); err != nil {
		return "参数不是合法的 JSON：" + err.Error()
	}

	since, until, err := parseRange(input.StartDate, input.EndDate)
	if err != nil {
		return err.Error()
	}

	search := store.MinuteSearch{
		UserID:  r.scope.userID,
		IDs:     r.scope.ids,
		Since:   later(since, r.scope.since),
		Until:   earlier(until, r.scope.until),
		Keyword: strings.TrimSpace(input.Keyword),
		Limit:   defaultSearchLimit,
	}

	if input.Limit > 0 {
		search.Limit = min(input.Limit, maxSearchLimit)
	}

	var (
		records []*store.Minute
		total   int
	)

	records, total, err = r.store.SearchMinutes(ctx, search)

	stepText := "检索" + rangeText(search.Since, search.Until)
	if search.Keyword != "" {
		stepText += "，关键词「" + search.Keyword + "」"
	}

	if err != nil {
		r.addStep(StepSearch, stepText+"，检索失败")

		return "检索失败：" + err.Error()
	}

	r.addStep(StepSearch, fmt.Sprintf("%s，找到 %d 条", stepText, total))

	if total == 0 {
		return "没有符合条件的妙记"
	}

	var result strings.Builder

	_, _ = fmt.Fprintf(&result, "共 %d 条符合条件，按时间倒序列出 %d 条：\n", total, len(records))

	for _, record := range records {
		r.remember(record)

		result.WriteString("- ")
		result.WriteString(r.describe(record))
		result.WriteString("\n")
	}

	return result.String()
}

// readMinutes 返回若干篇纪要的正文
func (r *run) readMinutes(ctx context.Context, arguments []byte) string {
	var input struct {
		IDs json.RawMessage `json:"ids"`
	}

	if err := json.Unmarshal(arguments, &input); err != nil {
		return "参数不是合法的 JSON：" + err.Error()
	}

	ids := parseIDs(input.IDs)
	if len(ids) == 0 {
		return "需要给出妙记编号"
	}

	var (
		result strings.Builder
		titles []string
	)

	if len(ids) > maxReadMinutes {
		_, _ = fmt.Fprintf(&result, "一次最多读 %d 条，只返回前 %d 条\n\n", maxReadMinutes, maxReadMinutes)

		ids = ids[:maxReadMinutes]
	}

	for _, id := range ids {
		record := r.scopedMinute(ctx, id)
		if record == nil {
			_, _ = fmt.Fprintf(&result, "## 编号 %d\n\n不在检索范围内\n\n", id)

			continue
		}

		titles = append(titles, titleOf(record))

		_, _ = fmt.Fprintf(&result, "## %s\n\n", r.describe(record))

		if record.Status != store.StatusDone || strings.TrimSpace(record.SummaryMarkdown) == "" {
			result.WriteString("纪要尚未生成，需要时用 read_transcript 阅读逐字稿\n\n")

			continue
		}

		result.WriteString(cleanSummary(record.SummaryMarkdown))
		result.WriteString("\n\n")
	}

	if len(titles) > 0 {
		r.addStep(StepRead, "阅读纪要："+strings.Join(titles, "、"))
	}

	return result.String()
}

// readTranscript 返回一段逐字稿
func (r *run) readTranscript(ctx context.Context, arguments []byte) string {
	var input struct {
		ID     json.RawMessage `json:"id"`
		Offset int             `json:"offset"`
	}

	if err := json.Unmarshal(arguments, &input); err != nil {
		return "参数不是合法的 JSON：" + err.Error()
	}

	ids := parseIDs(input.ID)
	if len(ids) == 0 {
		return "需要给出妙记编号"
	}

	record := r.scopedMinute(ctx, ids[0])
	if record == nil {
		return fmt.Sprintf("编号 %d 不在检索范围内", ids[0])
	}

	transcript, err := r.transcript(ctx, record)
	if err != nil {
		r.addStep(StepTranscript, "阅读逐字稿："+titleOf(record)+"，读取失败")

		return "读取逐字稿失败：" + err.Error()
	}

	if input.Offset == 0 {
		r.addStep(StepTranscript, "阅读逐字稿："+titleOf(record))
	}

	offset := max(input.Offset, 0)
	if offset >= len(transcript) {
		return fmt.Sprintf("逐字稿共 %d 字，已经读完", len(transcript))
	}

	end := min(offset+transcriptChunkRunes, len(transcript))
	header := fmt.Sprintf(
		"编号 %d「%s」的逐字稿，第 %d 至 %d 字，共 %d 字",
		record.ID,
		titleOf(record),
		offset+1,
		end,
		len(transcript),
	)

	if end < len(transcript) {
		header += fmt.Sprintf("，继续阅读时传 offset=%d", end)
	}

	return header + "：\n\n" + string(transcript[offset:end])
}

// transcript 读取并缓存一条妙记的逐字稿
func (r *run) transcript(ctx context.Context, record *store.Minute) ([]rune, error) {
	if cached, ok := r.transcripts[record.ID]; ok {
		return cached, nil
	}

	if r.service.transcripts == nil {
		return nil, errors.New("逐字稿不可用")
	}

	text, err := r.service.transcripts.Transcript(ctx, record)
	if err != nil {
		return nil, err
	}

	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil, errors.New("妙记没有逐字稿")
	}

	r.transcripts[record.ID] = runes

	return runes, nil
}

// describe 写出一条妙记的编号、时间、时长、标题与纪要状态，全部成员范围时附所有者
func (r *run) describe(record *store.Minute) string {
	parts := []string{"编号 " + strconv.FormatInt(record.ID, 10)}

	if !record.MinuteCreatedAt.IsZero() {
		parts = append(parts, formatMoment(record.MinuteCreatedAt))
	}

	if record.DurationSeconds > 0 {
		parts = append(parts, summary.FormatDuration(time.Duration(record.DurationSeconds)*time.Second))
	}

	parts = append(parts, titleOf(record))

	if r.scope.userID == 0 {
		parts = append(parts, "所有者 "+record.OwnerName)
	}

	if record.Status == store.StatusDone && record.SummaryMarkdown != "" {
		parts = append(parts, "纪要已生成")
	} else {
		parts = append(parts, "纪要未生成，可读逐字稿")
	}

	return strings.Join(parts, "｜")
}

// cleanSummary 去掉纪要里的链接、XML 标签、转义符与末尾由程序填写的两节
func cleanSummary(markdown string) string {
	if location := summaryTail.FindStringIndex(markdown); location != nil {
		markdown = markdown[:location[0]]
	}

	plain := escaped.ReplaceAllString(xmlTag.ReplaceAllString(markdownLink.ReplaceAllString(markdown, "$1"), ""), "$1")

	return strings.TrimSpace(blankLines.ReplaceAllString(plain, "\n\n"))
}

// parseIDs 解析编号，兼容数字、数字字符串与它们组成的数组
func parseIDs(raw json.RawMessage) []int64 {
	var values []any
	if err := json.Unmarshal(raw, &values); err != nil {
		var single any
		if err = json.Unmarshal(raw, &single); err != nil {
			return nil
		}

		values = []any{single}
	}

	var ids []int64

	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			ids = append(ids, int64(typed))
		case string:
			if id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(typed), "#"), 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
	}

	return ids
}

// parseRange 解析含当天的日期区间，返回的结束时间不含当时，空串表示不限
func parseRange(startDate, endDate string) (time.Time, time.Time, error) {
	var since, until time.Time

	if startDate = strings.TrimSpace(startDate); startDate != "" {
		parsed, err := time.ParseInLocation(dateLayout, startDate, time.Local)
		if err != nil {
			return since, until, errors.New("start_date 应为 YYYY-MM-DD 格式")
		}

		since = parsed
	}

	if endDate = strings.TrimSpace(endDate); endDate != "" {
		parsed, err := time.ParseInLocation(dateLayout, endDate, time.Local)
		if err != nil {
			return since, until, errors.New("end_date 应为 YYYY-MM-DD 格式")
		}

		until = parsed.AddDate(0, 0, 1)
	}

	return since, until, nil
}

// later 返回两个下限里较晚的那个，零值表示不限
func later(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.After(a)) {
		return b
	}

	return a
}

// earlier 返回两个上限里较早的那个，零值表示不限
func earlier(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}

	return a
}

// rangeText 写出检索步骤里的范围，例如「 11-10 至 11-15 的妙记」
func rangeText(since, until time.Time) string {
	now := time.Now()

	format := func(moment time.Time) string {
		if moment.Year() == now.Year() {
			return moment.Format("01-02")
		}

		return moment.Format(dateLayout)
	}

	switch {
	case !since.IsZero() && until.Equal(since.AddDate(0, 0, 1)):
		return " " + format(since) + " 的妙记"
	case !since.IsZero() && !until.IsZero():
		return " " + format(since) + " 至 " + format(until.AddDate(0, 0, -1)) + " 的妙记"
	case !since.IsZero():
		return " " + format(since) + " 以来的妙记"
	case !until.IsZero():
		return " " + format(until.AddDate(0, 0, -1)) + " 以前的妙记"
	default:
		return "全部妙记"
	}
}

// formatMoment 写出带星期的时间，例如「2026-11-12 周四 14:00」
func formatMoment(moment time.Time) string {
	return moment.Format(dateLayout) + " " + weekdays[moment.Weekday()] + " " + moment.Format("15:04")
}
