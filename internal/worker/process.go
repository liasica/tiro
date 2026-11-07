package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/llm"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/summary"
)

const (
	// 逐字稿还没生成时隔这么久再看一次，不计入尝试次数
	transcriptRetryDelay = 2 * time.Minute

	// 记录入库超过这么久逐字稿仍是空的，不再等待
	transcriptMaxWait = 6 * time.Hour
)

// skipError 表示这条妙记不需要生成纪要，原因写进记录
type skipError struct {
	reason string
}

func (e *skipError) Error() string {
	return e.reason
}

// errTranscriptPending 表示妙记的逐字稿还没生成，稍后再试
var errTranscriptPending = errors.New("妙记的逐字稿尚未生成")

// processPending 逐条处理到了时间的待处理记录，模型接口未配置时不处理
func (w *Worker) processPending(ctx context.Context, config *store.Config) {
	if !config.LLMConfigured() {
		return
	}

	for ctx.Err() == nil {
		record, err := w.store.NextPendingMinute(ctx, time.Now())
		if errors.Is(err, store.ErrNotFound) {
			return
		}

		if err != nil {
			slog.Error("读取待处理记录失败", "error", err)

			return
		}

		// 数据库出错时结束这一轮，避免对同一条记录原地打转
		if !w.process(ctx, config, record.ID) {
			return
		}
	}
}

// process 处理一条记录并把结果写回，数据库读写出错时返回 false
func (w *Worker) process(ctx context.Context, config *store.Config, recordID int64) bool {
	claimed, err := w.store.ClaimMinute(ctx, recordID)
	if err != nil {
		slog.Error("领取待处理记录失败", "id", recordID, "error", err)

		return false
	}

	// 记录刚被别处改了状态，放弃这一条
	if !claimed {
		return true
	}

	// 领取之后重新读一遍，拿到最新的内容
	var record *store.Minute

	record, err = w.store.Minute(ctx, recordID)
	if err != nil {
		slog.Error("读取记录失败", "id", recordID, "error", err)

		return false
	}

	var user *store.User

	user, err = w.store.User(ctx, record.UserID)
	if err != nil {
		slog.Error("读取记录的所有者失败", "id", recordID, "error", err)

		return false
	}

	w.settle(ctx, config, user, record, w.generate(ctx, config, user, record))

	// 进程退出时 ctx 已取消，换一个不受影响的上下文把状态写回去
	if err = w.store.UpdateMinute(context.WithoutCancel(ctx), record); err != nil {
		slog.Error("写回记录失败", "id", recordID, "error", err)

		return false
	}

	return true
}

// settle 按生成结果决定记录的下一个状态
func (w *Worker) settle(ctx context.Context, config *store.Config, user *store.User, record *store.Minute, cause error) {
	var skipped *skipError

	switch {
	case cause == nil:
		record.Status = store.StatusDone
		record.FinishedAt = time.Now()

		slog.Info("纪要已生成", "user", user.Name, "title", record.DocTitle)
	case errors.As(cause, &skipped):
		record.Status = store.StatusSkipped
		record.Reason = skipped.reason
		record.FinishedAt = time.Now()
	case errors.Is(cause, feishu.ErrDocumentUncertain):
		// 文档可能已经建好，自动重试会留下重复文档，交给人来判断
		record.Status = store.StatusFailed
		record.Reason = cause.Error() + "，到飞书云空间确认后再决定是否重新生成"
		record.FinishedAt = time.Now()

		slog.Warn("纪要文档的创建结果未知", "title", record.Title, "error", cause)
		w.notifyFailure(context.WithoutCancel(ctx), config, user, record)
	case errors.Is(cause, errTranscriptPending) && time.Since(record.CreatedAt) < transcriptMaxWait:
		// 等逐字稿不算一次失败
		record.Attempts--

		record.Status = store.StatusPending
		record.Reason = cause.Error()
		record.NextAttemptAt = time.Now().Add(transcriptRetryDelay)
	case errors.Is(cause, errTranscriptPending):
		record.Status = store.StatusSkipped
		record.Reason = "妙记一直没有逐字稿"
		record.FinishedAt = time.Now()
	case errors.Is(cause, ErrReauthorize), ctx.Err() != nil:
		// 授权失效或进程正在退出都不算一次失败，记录留在队列里
		record.Status = store.StatusPending
		record.Attempts--
	case record.Attempts >= maxAttempts:
		record.Status = store.StatusFailed
		record.Reason = cause.Error()
		record.FinishedAt = time.Now()

		slog.Warn("纪要生成失败", "title", record.Title, "error", cause)
		w.notifyFailure(ctx, config, user, record)
	default:
		record.Status = store.StatusPending
		record.Reason = cause.Error()
		record.NextAttemptAt = time.Now().Add(retryBackoff[min(record.Attempts, len(retryBackoff))-1])

		slog.Warn("纪要生成出错，稍后重试", "title", record.Title, "error", cause)
	}
}

// generate 完成一条妙记从取内容到发通知的全过程，过程中取得的信息直接写进 record
func (w *Worker) generate(ctx context.Context, config *store.Config, user *store.User, record *store.Minute) error {
	accessToken, err := w.AccessToken(ctx, config, user)
	if err != nil {
		return err
	}

	// 上一次已经生成出正文、只是建文档失败时，直接复用正文，不再调用模型
	if record.SummaryMarkdown == "" || record.DocTitle == "" {
		if err = w.summarize(ctx, config, accessToken, record); err != nil {
			return w.handleAuthError(ctx, config, user, err)
		}

		if err = w.store.UpdateMinute(ctx, record); err != nil {
			return err
		}
	}

	var document *feishu.Document

	document, err = w.createDocument(ctx, config, user, accessToken, record)
	if err != nil {
		return w.handleAuthError(ctx, config, user, fmt.Errorf("创建飞书文档失败: %w", err))
	}

	record.DocURL = document.URL

	// 只发文档链接，飞书客户端会渲染成带正文预览的文档卡片
	if err = w.feishu.SendText(ctx, credentialOf(config), user.OpenID, document.URL); err != nil {
		slog.Warn("发送飞书通知失败", "user", user.Name, "error", err)
	}

	return nil
}

// summarize 取回妙记内容并调用模型，结果写进 record 的标题与正文
func (w *Worker) summarize(ctx context.Context, config *store.Config, accessToken string, record *store.Minute) error {
	info, err := w.feishu.MinuteInfo(ctx, accessToken, record.MinuteToken)
	if err != nil {
		return fmt.Errorf("读取妙记信息失败: %w", err)
	}

	record.Title = info.Title
	record.MinuteURL = info.URL
	record.MinuteCreatedAt = info.CreatedAt
	record.DurationSeconds = int64(info.Duration.Seconds())

	var artifacts *feishu.MinuteArtifacts

	artifacts, err = w.feishu.MinuteArtifacts(ctx, accessToken, record.MinuteToken)
	if err != nil {
		return fmt.Errorf("读取妙记内容失败: %w", err)
	}

	transcript := strings.TrimSpace(artifacts.Transcript)
	if transcript == "" {
		return errTranscriptPending
	}

	if chars := utf8.RuneCountInString(transcript); chars < config.MinTranscriptChars {
		return &skipError{reason: fmt.Sprintf("逐字稿过短，共 %d 字", chars)}
	}

	model := llm.Config{BaseURL: config.LLMBaseURL, APIKey: config.LLMAPIKey, Model: config.LLMModel}
	input := summary.Input{
		Title:         info.Title,
		MinuteURL:     info.URL,
		StartedAt:     info.CreatedAt,
		Duration:      info.Duration,
		Glossary:      config.Glossary,
		FeishuSummary: artifacts.Summary,
		Transcript:    transcript,
	}

	var (
		result *summary.Result
		usage  llm.Usage
	)

	result, usage, err = summary.Generate(ctx, model, config.Prompt, input)

	w.recordUsage(ctx, record.ID, usage, err)

	if err != nil {
		return err
	}

	record.DocTitle = result.Title
	record.SummaryMarkdown = result.Markdown

	return nil
}

// recordUsage 记下一次模型调用的用量，出错且没有产生用量的调用不记
//
// 写入失败只记日志，不影响纪要生成
func (w *Worker) recordUsage(ctx context.Context, minuteID int64, usage llm.Usage, cause error) {
	if cause != nil && usage.TotalTokens == 0 {
		return
	}

	call := &store.LLMCall{
		MinuteID:         minuteID,
		Model:            usage.Model,
		PromptTokens:     usage.PromptTokens,
		CachedTokens:     usage.CachedTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		TotalTokens:      usage.TotalTokens,
		DurationMillis:   usage.Duration.Milliseconds(),
	}

	if cause != nil {
		call.Error = cause.Error()
	}

	// 进程退出时 ctx 已取消，已经产生的用量照样要记下
	if err := w.store.InsertLLMCall(context.WithoutCancel(ctx), call); err != nil {
		slog.Error("记录模型用量失败", "id", minuteID, "error", err)
	}
}

// createDocument 在用户云空间的纪要文件夹里新建文档
//
// 文件夹不存在时先创建；已记录的文件夹不可用（例如被删除）时新建一个再试一次
func (w *Worker) createDocument(ctx context.Context, config *store.Config, user *store.User, accessToken string, record *store.Minute) (*feishu.Document, error) {
	draft := feishu.DocumentDraft{
		Title:       record.DocTitle,
		Markdown:    record.SummaryMarkdown,
		FolderToken: user.FolderToken,
		Origin:      originOf(record.MinuteURL),
	}

	if draft.FolderToken != "" {
		document, err := w.feishu.CreateDocument(ctx, accessToken, draft)
		if err == nil {
			return document, nil
		}

		if !folderMissing(err) {
			return nil, err
		}

		slog.Warn("纪要文件夹不可用，重新创建", "user", user.Name, "error", err)
	}

	folderToken, err := w.feishu.CreateFolder(ctx, accessToken, config.FolderName)
	if err != nil {
		return nil, fmt.Errorf("创建文件夹「%s」失败: %w", config.FolderName, err)
	}

	if err = w.store.SetUserFolder(ctx, user.ID, folderToken); err != nil {
		return nil, err
	}

	user.FolderToken = folderToken
	draft.FolderToken = folderToken

	return w.feishu.CreateDocument(ctx, accessToken, draft)
}

// originOf 从妙记链接里取出租户的访问域名，取不出时返回空串
func originOf(minuteURL string) string {
	parsed, err := url.Parse(minuteURL)
	if err != nil || parsed.Host == "" {
		return ""
	}

	return parsed.Scheme + "://" + parsed.Host
}

// folderMissing 判断建文档失败是不是因为文件夹已经不存在
//
// 只在接口明确表示找不到父目录时才重建，权限类错误重建也无济于事
func folderMissing(err error) bool {
	if errors.Is(err, feishu.ErrDocumentUncertain) {
		return false
	}

	var apiErr *feishu.APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	if apiErr.HTTPStatus == http.StatusNotFound {
		return true
	}

	message := strings.ToLower(apiErr.Message)

	for _, hint := range []string{"not found", "not exist", "deleted", "parent"} {
		if strings.Contains(message, hint) {
			return true
		}
	}

	return false
}

// notifyFailure 在一条记录最终失败时私聊告知原因
func (w *Worker) notifyFailure(ctx context.Context, config *store.Config, user *store.User, record *store.Minute) {
	w.notify(ctx, config, user, feishu.Card{
		Title:      "会议纪要生成失败",
		Template:   "red",
		Markdown:   "**" + titleOf(record) + "**\n原因：" + record.Reason,
		ButtonText: "打开妙记",
		ButtonURL:  record.MinuteURL,
	})
}

func titleOf(record *store.Minute) string {
	if record.Title != "" {
		return record.Title
	}

	return record.MinuteToken
}
