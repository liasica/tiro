// Package bot 通过长连接接收用户私聊机器人的消息，用对话 Agent 回答并以卡片回复
package bot

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/liasica/tiro/internal/chat"
	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/store"
)

const (
	// 应用尚未配置时隔这么久再看一次
	configRetryDelay = 10 * time.Second

	// 断线重连的等待时间，从最短开始逐次翻倍
	minReconnectDelay = 5 * time.Second
	maxReconnectDelay = 2 * time.Minute

	// 连接保持超过这么久才算稳定，下次断线从最短等待时间开始
	stableConnection = time.Minute

	// 消息去重的保留时长，飞书在没收到应答时会重推
	dedupeWindow = 10 * time.Minute

	eventMessageReceive = "im.message.receive_v1"
)

// 结束当前对话的指令
var endCommands = []string{"新对话", "结束", "结束对话", "/new"}

// Recorder 把对话写进多维表格，返回多维表格链接
type Recorder interface {
	RecordChat(ctx context.Context, chatID int64) (string, error)
}

// Status 是长连接的状态
type Status struct {
	Connected   bool
	Error       string
	ConnectedAt time.Time
}

// Bot 负责机器人私聊对话
type Bot struct {
	store    *store.Store
	feishu   *feishu.Client
	chat     *chat.Service
	recorder Recorder

	mutex  sync.Mutex
	status Status
	// seen 记录处理过的消息 ID 与时间，用于去重
	seen map[string]time.Time

	// userLocks 让同一个用户的提问依次处理，几乎同时发来的两条不会各开一个对话
	userLocks sync.Map
	// inflight 记录正在处理的消息，退出时等它们把回答存好
	inflight sync.WaitGroup
}

// New 创建 Bot
func New(database *store.Store, client *feishu.Client, service *chat.Service, recorder Recorder) *Bot {
	return &Bot{
		store:    database,
		feishu:   client,
		chat:     service,
		recorder: recorder,
		seen:     make(map[string]time.Time),
	}
}

// Status 返回长连接的当前状态
func (b *Bot) Status() Status {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.status
}

// Run 保持长连接直到 ctx 结束，断线后自动重连；返回前等正在处理的消息收尾
func (b *Bot) Run(ctx context.Context) {
	defer b.inflight.Wait()

	failures := 0

	for ctx.Err() == nil {
		config, err := b.store.Config(ctx)
		if err != nil || !config.AppConfigured() {
			if !sleep(ctx, configRetryDelay) {
				return
			}

			continue
		}

		credential := feishu.AppCredential{AppID: config.AppID, AppSecret: config.AppSecret}
		startedAt := time.Now()

		err = b.feishu.ListenEvents(ctx, credential, b.connected, b.handle)
		if ctx.Err() != nil {
			return
		}

		b.disconnected(err)

		if time.Since(startedAt) > stableConnection {
			failures = 0
		}

		if !sleep(ctx, reconnectDelay(failures)) {
			return
		}

		failures++
	}
}

// reconnectDelay 是连续失败 n 次后的重连等待时间
func reconnectDelay(failures int) time.Duration {
	return min(minReconnectDelay<<min(failures, 8), maxReconnectDelay)
}

func (b *Bot) connected() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.status = Status{Connected: true, ConnectedAt: time.Now()}

	slog.Info("机器人长连接已建立")
}

func (b *Bot) disconnected(cause error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.status = Status{}

	if cause != nil {
		b.status.Error = cause.Error()
	}

	slog.Warn("机器人长连接断开", "error", cause)
}

// sleep 等待一段时间，ctx 先结束时返回 false
func sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// messageEvent 是接收消息事件里用到的字段
type messageEvent struct {
	Header struct {
		EventType string `json:"event_type"`
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
			SenderType string `json:"sender_type"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			ChatType    string `json:"chat_type"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
		} `json:"message"`
	} `json:"event"`
}

// handle 处理长连接收到的一条事件，只关心用户发给机器人的单聊消息
func (b *Bot) handle(ctx context.Context, payload []byte) {
	b.inflight.Add(1)
	defer b.inflight.Done()

	var event messageEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		slog.Warn("解析飞书事件失败", "error", err)

		return
	}

	message := event.Event.Message
	openID := event.Event.Sender.SenderID.OpenID

	if event.Header.EventType != eventMessageReceive || message.ChatType != "p2p" || openID == "" {
		return
	}

	if event.Event.Sender.SenderType != "" && event.Event.Sender.SenderType != "user" {
		return
	}

	if !b.firstSeen(message.MessageID) {
		return
	}

	config, err := b.store.Config(ctx)
	if err != nil {
		slog.Error("读取配置失败", "error", err)

		return
	}

	credential := feishu.AppCredential{AppID: config.AppID, AppSecret: config.AppSecret}

	text, ok := messageText(message.MessageType, message.Content)
	if !ok {
		b.reply(ctx, credential, openID, "只支持文字提问")

		return
	}

	b.converse(ctx, config, openID, text)
}

// firstSeen 判断消息是否第一次收到，顺带清理过期的记录
func (b *Bot) firstSeen(messageID string) bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	now := time.Now()

	for id, seenAt := range b.seen {
		if now.Sub(seenAt) > dedupeWindow {
			delete(b.seen, id)
		}
	}

	if _, ok := b.seen[messageID]; ok {
		return false
	}

	b.seen[messageID] = now

	return true
}

// messageText 取出文本消息或富文本消息里的文字，其他类型返回 false
func messageText(messageType, content string) (string, bool) {
	switch messageType {
	case "text":
		var text struct {
			Text string `json:"text"`
		}

		if err := json.Unmarshal([]byte(content), &text); err != nil {
			return "", false
		}

		return strings.TrimSpace(text.Text), true
	case "post":
		var post struct {
			Title   string `json:"title"`
			Content [][]struct {
				Tag  string `json:"tag"`
				Text string `json:"text"`
			} `json:"content"`
		}

		if err := json.Unmarshal([]byte(content), &post); err != nil {
			return "", false
		}

		lines := []string{post.Title}

		for _, paragraph := range post.Content {
			var line strings.Builder

			for _, element := range paragraph {
				if element.Tag == "text" || element.Tag == "a" {
					line.WriteString(element.Text)
				}
			}

			lines = append(lines, line.String())
		}

		return strings.TrimSpace(strings.Join(lines, "\n")), true
	default:
		return "", false
	}
}

// converse 回答用户的一条提问，或执行结束对话的指令
func (b *Bot) converse(ctx context.Context, config *store.Config, openID, text string) {
	credential := feishu.AppCredential{AppID: config.AppID, AppSecret: config.AppSecret}

	user, err := b.store.UserByOpenID(ctx, openID)
	if errors.Is(err, store.ErrNotFound) {
		b.reply(ctx, credential, openID, "先在 tiro 后台用飞书扫码登录后再提问")

		return
	}

	if err != nil {
		slog.Error("读取用户失败", "error", err)

		return
	}

	if text == "" {
		return
	}

	lock, _ := b.userLocks.LoadOrStore(user.ID, &sync.Mutex{})

	userMutex, _ := lock.(*sync.Mutex)
	userMutex.Lock()

	defer userMutex.Unlock()

	for _, command := range endCommands {
		if strings.EqualFold(text, command) {
			b.endChat(ctx, credential, user)

			return
		}
	}

	model, ok := config.ChatModel("")
	if !ok {
		b.reply(ctx, credential, openID, chat.ErrNoModel.Error())

		return
	}

	var current *store.Chat

	if current, err = b.openChat(ctx, user, model); err != nil {
		slog.Error("读取机器人对话失败", "error", err)

		return
	}

	unlock, locked := b.chat.Lock(current.ID)
	if !locked {
		b.reply(ctx, credential, openID, "上一个问题还在回答中")

		return
	}

	defer unlock()

	b.answer(ctx, credential, chat.Request{User: user, Chat: current, Question: text, Model: model})
}

// openChat 返回用户在机器人里未结束的对话，没有时新建一个
func (b *Bot) openChat(ctx context.Context, user *store.User, model store.ChatModel) (*store.Chat, error) {
	current, err := b.store.OpenChat(ctx, user.ID, store.ChatSourceBot)
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return current, err
	}

	current = &store.Chat{UserID: user.ID, Source: store.ChatSourceBot, ModelID: model.ID}

	if err = b.store.CreateChat(ctx, current); err != nil {
		return nil, err
	}

	return current, nil
}

// endChat 结束用户在机器人里的对话
func (b *Bot) endChat(ctx context.Context, credential feishu.AppCredential, user *store.User) {
	current, err := b.store.OpenChat(ctx, user.ID, store.ChatSourceBot)
	if err == nil {
		err = b.store.EndChat(ctx, current.ID)
	}

	if err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Error("结束机器人对话失败", "error", err)

		return
	}

	b.reply(ctx, credential, user.OpenID, "已结束当前对话，下一条提问开始新的对话")
}

// answer 先回一张进度卡片，回答过程中持续更新，完成后换成带引用来源的最终卡片
func (b *Bot) answer(ctx context.Context, credential feishu.AppCredential, request chat.Request) {
	openID := request.User.OpenID

	progress := newProgress()

	messageID, err := b.feishu.SendRawCard(ctx, credential, openID, progress.card())
	if err != nil {
		slog.Warn("发送回答卡片失败", "user", request.User.Name, "error", err)
	}

	stopUpdates := make(chan struct{})
	updatesDone := make(chan struct{})

	go func() {
		defer close(updatesDone)

		if messageID != "" {
			progress.keepUpdated(ctx, stopUpdates, func(card any) error {
				return b.feishu.UpdateCard(ctx, credential, messageID, card)
			})
		}
	}()

	var answer *store.ChatMessage

	answer, err = b.chat.Ask(ctx, request, chat.Callbacks{
		Step:  progress.addStep,
		Delta: progress.addDelta,
		Reset: progress.reset,
	})

	close(stopUpdates)
	<-updatesDone

	if err != nil {
		slog.Error("保存机器人对话失败", "error", err)

		answer = &store.ChatMessage{Error: "回答保存失败"}
	}

	// 回答已存进对话，写多维表格要等它保存之后
	var bitableURL string

	if answer.ID != 0 {
		if bitableURL, err = b.recorder.RecordChat(ctx, request.Chat.ID); err != nil {
			slog.Warn("写入多维表格失败", "chat", request.Chat.ID, "error", err)
		}
	}

	final := answerCard(answer, bitableURL)
	finishCtx := context.WithoutCancel(ctx)

	if messageID != "" {
		if err = b.feishu.UpdateCard(finishCtx, credential, messageID, final); err == nil {
			return
		}

		slog.Warn("更新回答卡片失败，改为另发一张", "error", err)
	}

	if _, err = b.feishu.SendRawCard(finishCtx, credential, openID, final); err != nil {
		slog.Warn("发送回答卡片失败", "user", request.User.Name, "error", err)
	}
}

// reply 回一条文本消息，失败只记日志
func (b *Bot) reply(ctx context.Context, credential feishu.AppCredential, openID, text string) {
	if err := b.feishu.SendText(ctx, credential, openID, text); err != nil {
		slog.Warn("回复机器人消息失败", "error", err)
	}
}
