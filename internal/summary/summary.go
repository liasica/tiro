// Package summary 把妙记内容交给模型写成会议纪要
package summary

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/liasica/tiro/internal/llm"
)

//go:embed prompt.md
var defaultPrompt string

// 模型输出里参与人一行的前缀
const participantsPrefix = "参与人："

var weekdays = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// DefaultPrompt 返回内置的纪要提示词
func DefaultPrompt() string {
	return strings.TrimSpace(defaultPrompt)
}

// Input 是生成一篇纪要所需的全部材料
type Input struct {
	Title         string
	MinuteURL     string
	StartedAt     time.Time
	Duration      time.Duration
	Glossary      string
	FeishuSummary string
	Transcript    string
}

// Result 是生成的纪要
type Result struct {
	// Title 是文档标题，由会议主题与日期组成
	Title string
	// Markdown 是文档正文，开头带时间、参与人与妙记链接
	Markdown string
}

// Generate 调用模型生成纪要，prompt 为空时使用内置提示词
func Generate(ctx context.Context, config llm.Config, prompt string, input Input) (*Result, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = DefaultPrompt()
	}

	reply, err := llm.Chat(ctx, config, prompt, buildUserMessage(input))
	if err != nil {
		return nil, err
	}

	return compose(input, reply), nil
}

// buildUserMessage 把会议信息、术语表、飞书总结与逐字稿拼成给模型的输入
func buildUserMessage(input Input) string {
	var message strings.Builder

	message.WriteString("# 会议信息\n\n")
	_, _ = fmt.Fprintf(&message, "- 妙记标题：%s\n", input.Title)

	if !input.StartedAt.IsZero() {
		startedAt := input.StartedAt.Format("2006-01-02 15:04")

		_, _ = fmt.Fprintf(&message, "- 开始时间：%s（%s）\n", startedAt, weekdays[input.StartedAt.Weekday()])
	}

	if input.Duration > 0 {
		_, _ = fmt.Fprintf(&message, "- 时长：%s\n", FormatDuration(input.Duration))
	}

	// 术语表与飞书总结为空时整节不给，免得模型在纪要里提到「没有术语表」
	writeSection(&message, "术语表", input.Glossary)
	writeSection(&message, "飞书自动生成的总结", input.FeishuSummary)
	writeSection(&message, "逐字稿", input.Transcript)

	return message.String()
}

// writeSection 追加一个一级标题小节，内容为空时跳过
func writeSection(message *strings.Builder, title, text string) {
	if text = strings.TrimSpace(text); text == "" {
		return
	}

	_, _ = fmt.Fprintf(message, "\n# %s\n\n%s\n", title, text)
}

// compose 从模型输出里取出主题与参与人，拼成最终的文档标题与正文
//
// 模型没有按约定输出主题或参与人时，退回妙记标题并省略参与人
func compose(input Input, reply string) *Result {
	lines := strings.Split(stripCodeFence(reply), "\n")
	topic := strings.TrimSpace(input.Title)
	participants := ""

	// 主题与参与人只会出现在正文开头，逐行消费直到遇到其他内容
	for len(lines) > 0 {
		line := strings.TrimSpace(lines[0])

		switch {
		case line == "":
		case strings.HasPrefix(line, "# "):
			if heading := strings.TrimSpace(strings.TrimPrefix(line, "# ")); heading != "" {
				topic = heading
			}
		case strings.HasPrefix(line, participantsPrefix):
			participants = strings.TrimSpace(strings.TrimPrefix(line, participantsPrefix))
		default:
			return finish(input, topic, participants, strings.Join(lines, "\n"))
		}

		lines = lines[1:]
	}

	return finish(input, topic, participants, "")
}

func finish(input Input, topic, participants, body string) *Result {
	if topic == "" {
		topic = "会议纪要"
	}

	title := topic

	if !input.StartedAt.IsZero() {
		title += " " + input.StartedAt.Format("2006-01-02")
	}

	var markdown strings.Builder

	// 头部信息由程序填写，不依赖模型
	if !input.StartedAt.IsZero() {
		startedAt := input.StartedAt.Format("2006-01-02 15:04")

		_, _ = fmt.Fprintf(&markdown, "> 时间：%s（%s）", startedAt, weekdays[input.StartedAt.Weekday()])

		if input.Duration > 0 {
			_, _ = fmt.Fprintf(&markdown, "，时长 %s", FormatDuration(input.Duration))
		}

		markdown.WriteString("\n>\n")
	}

	if participants != "" {
		_, _ = fmt.Fprintf(&markdown, "> 参与人：%s\n>\n", participants)
	}

	if input.MinuteURL != "" {
		_, _ = fmt.Fprintf(&markdown, "> 妙记：[%s](%s)\n", orTitle(input.Title), input.MinuteURL)
	}

	markdown.WriteString("\n")
	markdown.WriteString(strings.TrimSpace(body))
	markdown.WriteString("\n")

	return &Result{Title: title, Markdown: markdown.String()}
}

func orTitle(title string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}

	return "录音与逐字稿"
}

// stripCodeFence 去掉模型把整篇正文包进代码块时的首尾围栏
func stripCodeFence(reply string) string {
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

// FormatDuration 把时长写成「1 小时 57 分 11 秒」的形式
func FormatDuration(duration time.Duration) string {
	total := int(duration.Round(time.Second).Seconds())
	hours, minutes, seconds := total/3600, total%3600/60, total%60

	var parts []string

	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d 小时", hours))
	}

	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d 分", minutes))
	}

	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d 秒", seconds))
	}

	return strings.Join(parts, " ")
}
