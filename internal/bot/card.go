package bot

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/liasica/tiro/internal/chat"
	"github.com/liasica/tiro/internal/store"
)

const (
	// 进度卡片最快多久更新一次，飞书限制单条消息的更新频率
	updateInterval = 1200 * time.Millisecond

	// 正文的字节数上限，换成链接与引用来源后卡片整体也不超过 30 KB
	maxCardBytes = 12000
)

// progress 是回答过程中的卡片内容
type progress struct {
	mutex sync.Mutex
	steps []string
	draft strings.Builder
	dirty bool
}

func newProgress() *progress {
	return &progress{}
}

func (p *progress) addStep(step store.ChatStep) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.steps = append(p.steps, step.Text)
	p.dirty = true
}

func (p *progress) addDelta(text string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.draft.WriteString(text)
	p.dirty = true
}

func (p *progress) reset() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.draft.Reset()
	p.dirty = true
}

// card 返回当前进度的卡片
func (p *progress) card() any {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.dirty = false

	var elements []map[string]any

	if len(p.steps) > 0 {
		lines := make([]string, 0, len(p.steps))
		for _, step := range p.steps {
			lines = append(lines, "<font color='grey'>· "+step+"</font>")
		}

		elements = append(elements, markdown(strings.Join(lines, "\n")))
	}

	if draft := strings.TrimSpace(chat.StripRawCitations(p.draft.String())); draft != "" {
		elements = append(elements, markdown(truncate(draft)))
	}

	elements = append(elements, markdown("<font color='grey'>正在回答</font>"))

	return cardOf(elements)
}

// keepUpdated 按固定节拍把有变化的进度推到卡片上，直到 stop 关闭
func (p *progress) keepUpdated(ctx context.Context, stop <-chan struct{}, update func(card any) error) {
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			p.mutex.Lock()
			dirty := p.dirty
			p.mutex.Unlock()

			if !dirty {
				continue
			}

			if err := update(p.card()); err != nil {
				slog.Warn("更新回答卡片失败", "error", err)
			}
		}
	}
}

// answerCard 返回最终的回答卡片：正文里的引用换成跳转链接，后面列出引用来源与结果文档
func answerCard(answer *store.ChatMessage) any {
	sources := make(map[int]store.ChatSource, len(answer.Sources))
	for _, source := range answer.Sources {
		sources[source.Index] = source
	}

	body := chat.ReplaceCitations(truncate(answer.Content), func(index int, moment string) string {
		source, ok := sources[index]
		if !ok {
			return ""
		}

		return "[〔" + strconv.Itoa(index) + "〕](" + citationURL(source, moment) + ")"
	})

	var elements []map[string]any

	if body = strings.TrimSpace(body); body != "" {
		elements = append(elements, markdown(body))
	}

	if answer.Error != "" {
		elements = append(elements, markdown("<font color='red'>"+answer.Error+"</font>"))
	}

	if len(answer.Sources) > 0 {
		elements = append(elements, map[string]any{"tag": "hr"}, markdown(sourceList(answer.Sources)))
	}

	if result := resultLine(answer.Result); result != "" {
		elements = append(elements, markdown(result))
	}

	if footer := footerOf(answer); footer != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": footer, "text_size": "notation"})
	}

	return cardOf(elements)
}

// citationURL 是引用角标的跳转地址：带时间点的跳到妙记对应位置，否则打开纪要，没有纪要时打开妙记
func citationURL(source store.ChatSource, moment string) string {
	for _, item := range source.Moments {
		if moment != "" && item.Label == moment && item.URL != "" {
			return item.URL
		}
	}

	if moment == "" && source.DocURL != "" {
		return source.DocURL
	}

	return source.MinuteURL
}

// sourceList 写出引用来源，每条一行：序号、标题、时间与各个链接
func sourceList(sources []store.ChatSource) string {
	lines := []string{"**引用来源**"}

	for _, source := range sources {
		title := plainTitle(source.Title)
		if link := citationURL(source, ""); link != "" {
			title = "[" + title + "](" + link + ")"
		}

		parts := []string{"〔" + strconv.Itoa(source.Index) + "〕" + title}

		if moment, err := time.Parse(time.RFC3339, source.MinuteCreatedAt); err == nil {
			parts = append(parts, moment.Local().Format("01-02 15:04"))
		}

		if source.DocURL != "" && source.MinuteURL != "" {
			parts = append(parts, "[妙记]("+source.MinuteURL+")")
		}

		for _, moment := range source.Moments {
			parts = append(parts, "["+moment.Label+"]("+moment.URL+")")
		}

		lines = append(lines, strings.Join(parts, " · "))
	}

	return strings.Join(lines, "\n")
}

// resultLine 写出结果文档的链接或保存失败的原因，还在整理、已跳过或不整理时返回空串
func resultLine(result store.ChatResult) string {
	switch result.State {
	case store.ResultDone:
		label := chat.ResultLabel(result.Kind)
		if label == "" {
			label = "飞书文档"
		}

		return "已保存为" + label + "：[" + plainTitle(result.Title) + "](" + result.URL + ")"
	case store.ResultFailed:
		return "<font color='grey'>结果文档保存失败：" + result.Error + "</font>"
	default:
		return ""
	}
}

// footerOf 写出模型与用量，结果文档还在整理时附上说明
func footerOf(answer *store.ChatMessage) string {
	var parts []string

	if answer.Model != "" {
		parts = append(parts, answer.Model)
	}

	if answer.TotalTokens > 0 {
		parts = append(parts, strconv.FormatInt(answer.TotalTokens, 10)+" Token")
	}

	if answer.Result.State == store.ResultPending {
		parts = append(parts, "正在整理成文档")
	}

	if len(parts) == 0 {
		return ""
	}

	return "<font color='grey'>" + strings.Join(parts, " · ") + "</font>"
}

// plainTitle 把标题里会被当成链接语法的方括号换成全角
func plainTitle(title string) string {
	return strings.NewReplacer("[", "［", "]", "］").Replace(title)
}

// truncate 按字节数截断正文，不截断在字符中间，也不留下写了一半的引用标记
func truncate(text string) string {
	if len(text) <= maxCardBytes {
		return text
	}

	cut := maxCardBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}

	text = text[:cut]

	if open := strings.LastIndex(text, "{{"); open >= 0 && !strings.Contains(text[open:], "}}") {
		text = text[:open]
	}

	return text + "…"
}

func markdown(content string) map[string]any {
	return map[string]any{"tag": "markdown", "content": content}
}

// cardOf 组装卡片，声明 update_multi 才能在发出后更新
func cardOf(elements []map[string]any) any {
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"body":   map[string]any{"elements": elements},
	}
}
