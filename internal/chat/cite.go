package chat

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/summary"
)

var (
	// citeGroup 匹配模型写的出处标记，例如 [#12]、[#12@03:14]，以及 [#12, #15] 这类合写
	citeGroup = regexp.MustCompile(`\[(#\d+(?:\s*@\s*[\d:.]+)?(?:\s*[,，、;；]\s*#?\d+(?:\s*@\s*[\d:.]+)?)*)\]`)

	// citeItem 匹配合写标记里的一项
	citeItem = regexp.MustCompile(`#?(\d+)(?:\s*@\s*([\d:.]+))?`)

	// storedCite 匹配保存下来的引用标记，例如 {{cite:1}}、{{cite:1@03:14}}
	storedCite = regexp.MustCompile(`\{\{cite:(\d+)(?:@([\d:]+))?\}\}`)
)

// cite 把模型输出里的出处标记换成 {{cite:序号}}，按首次出现的顺序给妙记编号，返回改写后的正文与引用来源
//
// 编号找不到或不可见的标记直接删掉
func (r *run) cite(ctx context.Context, content string) (string, []store.ChatSource) {
	var sources []store.ChatSource

	indexes := make(map[int64]int)

	rewritten := citeGroup.ReplaceAllStringFunc(content, func(group string) string {
		var markers strings.Builder

		written := make(map[string]bool)

		for _, item := range citeItem.FindAllStringSubmatch(group, -1) {
			minuteID, err := strconv.ParseInt(item[1], 10, 64)
			if err != nil {
				continue
			}

			record := r.visibleMinute(ctx, minuteID)
			if record == nil {
				continue
			}

			index, seen := indexes[minuteID]
			if !seen {
				index = len(sources) + 1
				indexes[minuteID] = index
				sources = append(sources, newSource(index, record))
			}

			marker := "{{cite:" + strconv.Itoa(index)

			if label := normalizeMoment(item[2]); label != "" {
				marker += "@" + label

				sources[index-1].Moments = addMoment(sources[index-1].Moments, label, record.MinuteURL)
			}

			marker += "}}"

			if !written[marker] {
				written[marker] = true

				markers.WriteString(marker)
			}
		}

		return markers.String()
	})

	return rewritten, sources
}

// newSource 用妙记记录构造一条引用来源
func newSource(index int, record *store.Minute) store.ChatSource {
	source := store.ChatSource{
		Index:           index,
		MinuteID:        record.ID,
		Title:           titleOf(record),
		Owner:           record.OwnerName,
		DurationSeconds: record.DurationSeconds,
		MinuteURL:       record.MinuteURL,
		DocURL:          record.DocURL,
		Moments:         []store.ChatMoment{},
	}

	if !record.MinuteCreatedAt.IsZero() {
		source.MinuteCreatedAt = record.MinuteCreatedAt.Format(time.RFC3339)
	}

	return source
}

// addMoment 追加一个引用时间点，已有的不重复添加
func addMoment(moments []store.ChatMoment, label, minuteURL string) []store.ChatMoment {
	for _, moment := range moments {
		if moment.Label == label {
			return moments
		}
	}

	return append(moments, store.ChatMoment{Label: label, URL: summary.TimestampURL(minuteURL, label)})
}

// normalizeMoment 把 00:12:05.200、12:05 这类时间统一写成 mm:ss，超过一小时写成 h:mm:ss，不合法时返回空串
func normalizeMoment(text string) string {
	text, _, _ = strings.Cut(strings.TrimSpace(text), ".")
	if text == "" {
		return ""
	}

	parts := strings.Split(text, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return ""
	}

	seconds := 0

	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return ""
		}

		seconds = seconds*60 + value
	}

	hours, minutes, rest := seconds/3600, seconds%3600/60, seconds%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, rest)
	}

	return fmt.Sprintf("%02d:%02d", minutes, rest)
}

// modelText 把保存下来的回答还原成模型的出处标记写法，作为后续轮次的历史
func modelText(message *store.ChatMessage) string {
	minuteIDs := make(map[int]int64, len(message.Sources))
	for _, source := range message.Sources {
		minuteIDs[source.Index] = source.MinuteID
	}

	return storedCite.ReplaceAllStringFunc(message.Content, func(marker string) string {
		match := storedCite.FindStringSubmatch(marker)

		index, _ := strconv.Atoi(match[1])

		minuteID, ok := minuteIDs[index]
		if !ok {
			return ""
		}

		text := "[#" + strconv.FormatInt(minuteID, 10)

		if match[2] != "" {
			text += "@" + match[2]
		}

		return text + "]"
	})
}

// ReplaceCitations 把正文里的 {{cite:序号}} 标记交给 render 改写，render 收到序号与时间点（可能为空）
func ReplaceCitations(content string, render func(index int, moment string) string) string {
	return storedCite.ReplaceAllStringFunc(content, func(marker string) string {
		match := storedCite.FindStringSubmatch(marker)

		index, _ := strconv.Atoi(match[1])

		return render(index, match[2])
	})
}

// StripRawCitations 去掉模型原始输出里的出处标记，用于流式显示
func StripRawCitations(content string) string {
	return citeGroup.ReplaceAllString(content, "")
}

func titleOf(record *store.Minute) string {
	if title := strings.TrimSpace(record.Title); title != "" {
		return title
	}

	return "未命名妙记"
}
