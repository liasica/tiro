package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/liasica/tiro/internal/chat"
	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/llm"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/summary"
)

const (
	// 定时检查待整理回答的节拍
	resultTick = time.Minute

	// 只整理这么久以内的回答
	resultLookback = 7 * 24 * time.Hour

	// 同时整理的回答数
	resultWorkers = 4

	// 一条回答最多自动尝试的次数，授权失效不计入
	maxResultAttempts = 3

	// 授权失效时隔这么久再试，重新扫码登录后自动补上
	reauthRetryInterval = 5 * time.Minute
)

// 第 n 次失败后等 resultRetryDelays[n-1] 再试
var resultRetryDelays = []time.Duration{time.Minute, 5 * time.Minute}

// Transcript 用妙记所有者的令牌读取逐字稿，供对话使用
func (w *Worker) Transcript(ctx context.Context, record *store.Minute) (string, error) {
	config, err := w.store.Config(ctx)
	if err != nil {
		return "", err
	}

	var owner *store.User

	if owner, err = w.store.User(ctx, record.UserID); err != nil {
		return "", err
	}

	var accessToken string

	if accessToken, err = w.AccessToken(ctx, config, owner); err != nil {
		return "", err
	}

	var artifacts *feishu.MinuteArtifacts

	artifacts, err = w.feishu.MinuteArtifacts(ctx, accessToken, record.MinuteToken)
	if err != nil {
		return "", w.handleAuthError(ctx, config, owner, err)
	}

	return artifacts.Transcript, nil
}

// RunResults 把回答整理成飞书文档：有新回答时立即整理，失败的按间隔重试，直到 ctx 结束
//
// 正在整理的回答跳过，返回前等进行中的整理收尾
func (w *Worker) RunResults(ctx context.Context, answered <-chan int64) {
	var running sync.WaitGroup
	defer running.Wait()

	slots := make(chan struct{}, resultWorkers)

	start := func(messageID int64) {
		running.Go(func() {
			slots <- struct{}{}

			defer func() { <-slots }()

			mutex := w.resultLock(messageID)
			if !mutex.TryLock() {
				return
			}

			defer mutex.Unlock()

			if _, err := w.saveResult(ctx, messageID); err != nil {
				slog.Error("整理结果文档失败", "message", messageID, "error", err)
			}
		})
	}

	ticker := time.NewTicker(resultTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case messageID := <-answered:
			start(messageID)
		case <-ticker.C:
			now := time.Now()

			messageIDs, err := w.store.DueResults(ctx, now.Add(-resultLookback), now)
			if err != nil {
				slog.Error("读取待整理的回答失败", "error", err)

				continue
			}

			for _, messageID := range messageIDs {
				start(messageID)
			}
		}
	}
}

// SaveResult 立即把一条回答整理成飞书文档，返回处理后的消息；同一条回答正在整理时等它完成
func (w *Worker) SaveResult(ctx context.Context, messageID int64) (*store.ChatMessage, error) {
	mutex := w.resultLock(messageID)
	mutex.Lock()

	defer mutex.Unlock()

	return w.saveResult(ctx, messageID)
}

func (w *Worker) resultLock(messageID int64) *sync.Mutex {
	lock, _ := w.resultLocks.LoadOrStore(messageID, &sync.Mutex{})

	mutex, _ := lock.(*sync.Mutex)

	return mutex
}

// resultDue 判断回答是否需要整理：还没整理过，或上次失败且到了重试时间
func resultDue(message *store.ChatMessage, now time.Time) bool {
	switch message.Result.State {
	case store.ResultPending:
		return true
	case store.ResultFailed:
		return !message.Result.NextAt.IsZero() && !message.Result.NextAt.After(now)
	default:
		return false
	}
}

// saveResult 整理一条回答：让模型判断结果类型并写成正文，再在提问人的纪要文件夹里建文档
//
// 正文生成后立即落库，建文档失败重试时直接复用；模型判断不值得保存的标为已跳过
func (w *Worker) saveResult(ctx context.Context, messageID int64) (*store.ChatMessage, error) {
	message, err := w.store.ChatMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}

	if !resultDue(message, time.Now()) {
		return message, nil
	}

	var job *resultJob

	if job, err = w.loadResultJob(ctx, message); err != nil {
		return nil, err
	}

	if err = w.runResultJob(ctx, job); err != nil {
		w.failResult(ctx, message, err)
	}

	return message, nil
}

// resultJob 是整理一条回答用到的配置、提问人、对话与提问
type resultJob struct {
	config   *store.Config
	owner    *store.User
	chat     *store.Chat
	question *store.ChatMessage
	message  *store.ChatMessage
}

func (w *Worker) loadResultJob(ctx context.Context, message *store.ChatMessage) (*resultJob, error) {
	config, err := w.store.Config(ctx)
	if err != nil {
		return nil, err
	}

	job := &resultJob{config: config, message: message}

	if job.chat, err = w.store.Chat(ctx, message.ChatID); err != nil {
		return nil, err
	}

	if job.owner, err = w.store.User(ctx, job.chat.UserID); err != nil {
		return nil, err
	}

	if job.question, err = w.store.ChatQuestion(ctx, message); err != nil {
		return nil, fmt.Errorf("读取回答对应的提问失败: %w", err)
	}

	return job, nil
}

// runResultJob 依次生成正文与建文档，结果写回 job.message
func (w *Worker) runResultJob(ctx context.Context, job *resultJob) error {
	message := job.message

	accessToken, err := w.AccessToken(ctx, job.config, job.owner)
	if err != nil {
		return err
	}

	if message.Result.Markdown == "" {
		var skipped bool

		if skipped, err = w.composeResult(ctx, job); err != nil || skipped {
			return err
		}
	}

	draft := feishu.DocumentDraft{
		Title:    message.Result.Title,
		Markdown: message.Result.Markdown,
		Origin:   originOf(firstMinuteURL(message.Sources)),
	}

	var document *feishu.Document

	document, err = w.createInFolder(ctx, job.config, job.owner, accessToken, draft)
	if err != nil {
		return w.handleAuthError(ctx, job.config, job.owner, fmt.Errorf("创建飞书文档失败: %w", err))
	}

	message.Result.State = store.ResultDone
	message.Result.URL = document.URL
	message.Result.Error = ""
	message.Result.NextAt = time.Time{}

	// 文档已经建好，进程退出时也要记下链接
	if err = w.store.SetMessageResult(context.WithoutCancel(ctx), message.ID, message.Result); err != nil {
		return err
	}

	slog.Info("结果文档已保存", "user", job.owner.Name, "title", message.Result.Title)

	return nil
}

// composeResult 调用模型整理正文并落库，回答不值得保存时标为已跳过并返回 true
func (w *Worker) composeResult(ctx context.Context, job *resultJob) (bool, error) {
	model, ok := job.config.ChatModel(job.chat.ModelID)
	if !ok {
		return false, chat.ErrNoModel
	}

	input := chat.ResultInput{
		Question: job.question.Content,
		AskedAt:  job.question.CreatedAt,
		Asker:    job.owner.Name,
		Scope:    job.question.Scope,
		Answer:   job.message,
	}

	// 早期的提问没有记下检索范围，按对话当前的范围
	if input.Scope == "" {
		input.Scope = chat.Describe(job.owner, job.chat.Context)
	}

	input.Topics = w.meetingTopics(ctx, job.message.Sources)

	config := llm.Config{BaseURL: model.BaseURL, APIKey: model.APIKey, Model: model.Model, API: model.API}

	doc, _, err := chat.ComposeResult(ctx, config, input)
	if err != nil {
		return false, fmt.Errorf("整理文档失败: %w", err)
	}

	result := &job.message.Result

	if doc.Kind == "" {
		result.State = store.ResultSkipped
		result.Error = ""
		result.NextAt = time.Time{}

		return true, w.store.SetMessageResult(context.WithoutCancel(ctx), job.message.ID, *result)
	}

	result.Kind = doc.Kind
	result.Title = doc.Title
	result.Markdown = doc.Markdown

	return false, w.store.SetMessageResult(context.WithoutCancel(ctx), job.message.ID, *result)
}

// meetingTopics 读出引用的妙记在纪要里的会议主题，读不到的跳过
func (w *Worker) meetingTopics(ctx context.Context, sources []store.ChatSource) map[int64]string {
	topics := make(map[int64]string, len(sources))

	for _, source := range sources {
		record, err := w.store.Minute(ctx, source.MinuteID)
		if err != nil {
			continue
		}

		if topic := summary.Topic(record.SummaryMarkdown); topic != "" {
			topics[source.MinuteID] = topic
		}
	}

	return topics
}

// failResult 记下失败原因与下次尝试的时间
//
// 授权失效不计入尝试次数，重新登录后自动补上；建文档结果未知时不再自动尝试，避免留下重复文档
func (w *Worker) failResult(ctx context.Context, message *store.ChatMessage, cause error) {
	result := &message.Result
	now := time.Now()

	result.State = store.ResultFailed
	result.Error = cause.Error()
	result.NextAt = time.Time{}

	switch {
	case errors.Is(cause, ErrReauthorize):
		result.Error = "飞书授权已失效，重新扫码登录后自动保存"
		result.NextAt = now.Add(reauthRetryInterval)
	case errors.Is(cause, feishu.ErrDocumentUncertain):
	default:
		result.Attempts++

		if result.Attempts < maxResultAttempts {
			result.NextAt = now.Add(resultRetryDelays[min(result.Attempts, len(resultRetryDelays))-1])
		}
	}

	slog.Warn(
		"保存结果文档失败",
		"message",
		message.ID,
		"attempts",
		result.Attempts,
		"error",
		cause,
	)

	if err := w.store.SetMessageResult(context.WithoutCancel(ctx), message.ID, *result); err != nil {
		slog.Error("记录结果文档的失败原因出错", "message", message.ID, "error", err)
	}
}

// firstMinuteURL 返回第一条引用的妙记链接，用来取租户的访问域名
func firstMinuteURL(sources []store.ChatSource) string {
	for _, source := range sources {
		if source.MinuteURL != "" {
			return source.MinuteURL
		}
	}

	return ""
}
