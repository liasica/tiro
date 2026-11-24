package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liasica/tiro/internal/chat"
	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/summary"
)

const (
	// 定时补写的节拍
	recordTick = time.Minute

	// 写入失败的回答隔这么久再试
	recordRetryInterval = 5 * time.Minute

	// 只补写这么久以内的回答
	recordLookback = 7 * 24 * time.Hour
)

// 多维表格里的表名与字段名
const (
	tableQA      = "问答"
	tableMinutes = "引用妙记"

	fieldQuestion = "问题"
	fieldAnswer   = "回答"
	fieldDetail   = "引用明细"
	fieldAsker    = "提问人"
	fieldAskedAt  = "提问时间"
	fieldScope    = "检索范围"
	fieldChannel  = "渠道"
	fieldModel    = "模型"
	fieldTokens   = "Token"
	fieldStatus   = "状态"
	fieldMinutes  = "引用妙记"

	fieldTitle     = "标题"
	fieldMinuteAt  = "妙记时间"
	fieldDuration  = "时长"
	fieldDoc       = "会议纪要"
	fieldMinuteURL = "妙记"
	fieldOwner     = "所有者"

	channelWeb = "网页"
	channelBot = "机器人"

	statusAnswered    = "已回答"
	statusFailed      = "出错"
	statusInterrupted = "已中断"
)

var (
	boldMarks    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	headingMarks = regexp.MustCompile(`(?m)^#{1,6}\s+`)
)

// exchange 是一轮问答
type exchange struct {
	question *store.ChatMessage
	answer   *store.ChatMessage
}

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

// RunRecorder 把对话的问答写进多维表格，有新回答时立即写，失败的定时补写，直到 ctx 结束
func (w *Worker) RunRecorder(ctx context.Context, answered <-chan int64) {
	ticker := time.NewTicker(recordTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case chatID := <-answered:
			w.recordQuietly(ctx, chatID)
		case <-ticker.C:
			w.recordPending(ctx)
		}
	}
}

// recordPending 补写到了重试时间的对话
func (w *Worker) recordPending(ctx context.Context) {
	now := time.Now()

	chatIDs, err := w.store.PendingRecordChats(ctx, now.Add(-recordLookback), now.Add(-recordRetryInterval))
	if err != nil {
		slog.Error("读取待写入多维表格的对话失败", "error", err)

		return
	}

	for _, chatID := range chatIDs {
		if ctx.Err() != nil {
			return
		}

		w.recordQuietly(ctx, chatID)
	}
}

func (w *Worker) recordQuietly(ctx context.Context, chatID int64) {
	if _, err := w.RecordChat(ctx, chatID); err != nil {
		slog.Warn("写入多维表格失败", "chat", chatID, "error", err)
	}
}

// RecordChat 把对话里还没写入多维表格的问答按顺序写进去，返回对话的多维表格链接
//
// 对话还没有多维表格时先在提问人的纪要文件夹里新建；失败原因记在消息上，之后定时补写
func (w *Worker) RecordChat(ctx context.Context, chatID int64) (string, error) {
	lock, _ := w.recordLocks.LoadOrStore(chatID, &sync.Mutex{})

	mutex, _ := lock.(*sync.Mutex)
	mutex.Lock()

	defer mutex.Unlock()

	current, err := w.store.Chat(ctx, chatID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	var messages []*store.ChatMessage

	if messages, err = w.store.ChatMessages(ctx, chatID); err != nil {
		return "", err
	}

	pending := pendingExchanges(messages)
	if len(pending) == 0 {
		return current.BitableURL, nil
	}

	if err = w.writeExchanges(ctx, current, pending); err != nil {
		w.failExchanges(ctx, pending, err)

		return current.BitableURL, err
	}

	return current.BitableURL, nil
}

// pendingExchanges 找出还没写入多维表格的问答
func pendingExchanges(messages []*store.ChatMessage) []exchange {
	var (
		pending  []exchange
		question *store.ChatMessage
	)

	for _, message := range messages {
		if message.Role == store.ChatRoleUser {
			question = message

			continue
		}

		if message.RecordID == "" && question != nil {
			pending = append(pending, exchange{question: question, answer: message})
		}
	}

	return pending
}

// writeExchanges 逐条写入问答，写成功的立即记下记录 ID；多维表格已被删除时清掉旧信息，新建一个再写
func (w *Worker) writeExchanges(ctx context.Context, current *store.Chat, pending []exchange) error {
	config, err := w.store.Config(ctx)
	if err != nil {
		return err
	}

	var owner *store.User

	if owner, err = w.store.User(ctx, current.UserID); err != nil {
		return err
	}

	var accessToken string

	if accessToken, err = w.AccessToken(ctx, config, owner); err != nil {
		return err
	}

	target := recordTarget{config: config, owner: owner, accessToken: accessToken, chat: current}

	err = w.writeToBase(ctx, target, pending)
	if feishu.IsBaseMissing(err) {
		slog.Warn("对话的多维表格已不存在，重新新建", "chat", current.ID, "error", err)

		current.ResetBitable()

		if err = w.store.SetChatBitable(ctx, current); err != nil {
			return err
		}

		err = w.writeToBase(ctx, target, pending)
	}

	if err != nil {
		return w.handleAuthError(ctx, config, owner, err)
	}

	return nil
}

// recordTarget 是一次写入用到的配置、提问人身份与对话
type recordTarget struct {
	config      *store.Config
	owner       *store.User
	accessToken string
	chat        *store.Chat
}

// writeToBase 准备好多维表格后逐条写入还没写过的问答
func (w *Worker) writeToBase(ctx context.Context, target recordTarget, pending []exchange) error {
	owner, accessToken, current := target.owner, target.accessToken, target.chat

	if err := w.prepareBase(ctx, target.config, owner, accessToken, current); err != nil {
		return err
	}

	for _, item := range pending {
		if item.answer.RecordID != "" {
			continue
		}

		recordID, err := w.writeExchange(ctx, accessToken, owner, current, item)
		if err != nil {
			return err
		}

		if err = w.store.SetMessageRecord(ctx, item.answer.ID, recordID, ""); err != nil {
			return err
		}

		// 后面的问答写入失败时，已经写成功的不能被当成失败
		item.answer.RecordID = recordID
	}

	return nil
}

// failExchanges 把失败原因记到还没写入的回答上
func (w *Worker) failExchanges(ctx context.Context, pending []exchange, cause error) {
	reason := cause.Error()

	switch {
	case errors.Is(cause, ErrReauthorize):
		reason = "飞书授权已失效，重新扫码登录后补写"
	case feishu.IsScopeMissing(cause):
		reason = "缺少多维表格权限，开通 bitable:app 并重新扫码登录后补写"
	}

	for _, item := range pending {
		// 前面的已经写成功，不覆盖
		if item.answer.RecordID != "" {
			continue
		}

		if err := w.store.SetMessageRecord(context.WithoutCancel(ctx), item.answer.ID, "", reason); err != nil {
			slog.Error("记录多维表格写入失败的原因出错", "message", item.answer.ID, "error", err)
		}
	}
}

// prepareBase 确保对话有多维表格、两张数据表与关联字段，每完成一步立即落库，中途失败时下次接着做
func (w *Worker) prepareBase(ctx context.Context, config *store.Config, owner *store.User, accessToken string, current *store.Chat) error {
	if current.BitableToken == "" {
		base, err := w.createBase(ctx, config, owner, accessToken, baseName(current))
		if err != nil {
			return fmt.Errorf("新建多维表格失败: %w", err)
		}

		current.BitableToken = base.Token
		current.BitableURL = base.URL
		current.BitableSpareTable = base.DefaultTableID

		if err = w.store.SetChatBitable(ctx, current); err != nil {
			return err
		}
	}

	if current.BitableQATable == "" {
		tableID, err := w.ensureTable(ctx, accessToken, current.BitableToken, qaTable())
		if err != nil {
			return err
		}

		current.BitableQATable = tableID

		if err = w.store.SetChatBitable(ctx, current); err != nil {
			return err
		}
	}

	// 新建的多维表格自带一张空数据表，问答表建好后删掉；删除失败只记日志，下次再试
	if current.BitableSpareTable != "" {
		err := w.feishu.DeleteBaseTable(ctx, accessToken, current.BitableToken, current.BitableSpareTable)
		if err == nil || feishu.IsBaseMissing(err) {
			current.BitableSpareTable = ""

			if err = w.store.SetChatBitable(ctx, current); err != nil {
				return err
			}
		} else {
			slog.Warn("删除多维表格自带的数据表失败", "chat", current.ID, "error", err)
		}
	}

	if current.BitableMinuteTable == "" {
		tableID, err := w.ensureTable(ctx, accessToken, current.BitableToken, minuteTable())
		if err != nil {
			return err
		}

		current.BitableMinuteTable = tableID

		if err = w.store.SetChatBitable(ctx, current); err != nil {
			return err
		}
	}

	if !current.BitableLinked {
		// 关联字段要等妙记表建好才能加，放在问答表最后一列；上次已加上但没收到结果时会报重名，视为已加上
		link := feishu.LinkField(fieldMinutes, current.BitableMinuteTable)

		err := w.feishu.CreateBaseField(ctx, accessToken, current.BitableToken, current.BitableQATable, link)
		if err != nil && !feishu.IsDuplicateName(err) {
			return fmt.Errorf("新建「%s」字段失败: %w", fieldMinutes, err)
		}

		current.BitableLinked = true

		if err = w.store.SetChatBitable(ctx, current); err != nil {
			return err
		}
	}

	return nil
}

// ensureTable 新建数据表；上次已建好但没收到结果时会报重名，此时按名称找到它
func (w *Worker) ensureTable(ctx context.Context, accessToken, baseToken string, table feishu.BaseTable) (string, error) {
	tableID, err := w.feishu.CreateBaseTable(ctx, accessToken, baseToken, table)
	if err == nil {
		return tableID, nil
	}

	if feishu.IsDuplicateName(err) {
		if tableID, err = w.feishu.FindBaseTable(ctx, accessToken, baseToken, table.Name); err == nil && tableID != "" {
			return tableID, nil
		}
	}

	return "", fmt.Errorf("新建「%s」数据表失败: %w", table.Name, err)
}

// createBase 在用户的纪要文件夹里新建多维表格，文件夹不存在时新建文件夹再试
func (w *Worker) createBase(ctx context.Context, config *store.Config, owner *store.User, accessToken, name string) (*feishu.Base, error) {
	if owner.FolderToken != "" {
		base, err := w.feishu.CreateBase(ctx, accessToken, name, owner.FolderToken)
		if err == nil || !folderMissing(err) {
			return base, err
		}

		slog.Warn("纪要文件夹不可用，重新创建", "user", owner.Name, "error", err)
	}

	folderToken, err := w.feishu.CreateFolder(ctx, accessToken, config.FolderName)
	if err != nil {
		return nil, fmt.Errorf("创建文件夹「%s」失败: %w", config.FolderName, err)
	}

	if err = w.store.SetUserFolder(ctx, owner.ID, folderToken); err != nil {
		return nil, err
	}

	owner.FolderToken = folderToken

	return w.feishu.CreateBase(ctx, accessToken, name, folderToken)
}

// baseName 是多维表格的名称，例如「对话记录：本周的待办 2026年11月24日」
func baseName(current *store.Chat) string {
	title := current.Title
	if title == "" {
		title = "对话"
	}

	return "对话记录：" + title + " " + current.CreatedAt.Format("2006年1月2日")
}

func qaTable() feishu.BaseTable {
	return feishu.BaseTable{
		Name:     tableQA,
		ViewName: "全部问答",
		Fields: []feishu.BaseField{
			feishu.TextField(fieldQuestion),
			feishu.TextField(fieldAnswer),
			feishu.TextField(fieldDetail),
			feishu.UserField(fieldAsker),
			feishu.DateField(fieldAskedAt),
			feishu.TextField(fieldScope),
			feishu.SelectField(fieldChannel, channelWeb, channelBot),
			feishu.TextField(fieldModel),
			feishu.NumberField(fieldTokens),
			feishu.SelectField(fieldStatus, statusAnswered, statusFailed, statusInterrupted),
		},
	}
}

func minuteTable() feishu.BaseTable {
	return feishu.BaseTable{
		Name:     tableMinutes,
		ViewName: "全部妙记",
		Fields: []feishu.BaseField{
			feishu.TextField(fieldTitle),
			feishu.DateField(fieldMinuteAt),
			feishu.TextField(fieldDuration),
			feishu.URLField(fieldDoc),
			feishu.URLField(fieldMinuteURL),
			feishu.TextField(fieldOwner),
		},
	}
}

// writeExchange 写入一轮问答，引用到的妙记先写进妙记表，同一场会议只写一次
func (w *Worker) writeExchange(ctx context.Context, accessToken string, owner *store.User, current *store.Chat, item exchange) (string, error) {
	var minuteRecords []string

	for _, source := range item.answer.Sources {
		recordID, ok := current.BitableMinutes[source.MinuteID]
		if !ok {
			var err error

			record := feishu.BaseRecord{
				TableID: current.BitableMinuteTable,
				// 同一个多维表格里同一场会议只写一次，重试时不会重复
				ClientToken: feishu.ClientToken("minute", current.BitableToken, strconv.FormatInt(source.MinuteID, 10)),
				Fields:      minuteFields(source),
			}

			recordID, err = w.feishu.CreateBaseRecord(ctx, accessToken, current.BitableToken, record)
			if err != nil {
				return "", fmt.Errorf("写入引用妙记失败: %w", err)
			}

			if current.BitableMinutes == nil {
				current.BitableMinutes = make(map[int64]string)
			}

			current.BitableMinutes[source.MinuteID] = recordID

			if err = w.store.SetChatBitable(ctx, current); err != nil {
				return "", err
			}
		}

		minuteRecords = append(minuteRecords, recordID)
	}

	values := []fieldValue{
		{fieldQuestion, item.question.Content},
		{fieldAnswer, textValue(plainAnswer(item.answer))},
		{fieldDetail, textValue(sourceDetail(item.answer.Sources))},
		{fieldAsker, feishu.UserValue(owner.OpenID)},
		{fieldAskedAt, item.question.CreatedAt.UnixMilli()},
		{fieldScope, scopeOf(owner, current, item.question)},
		{fieldChannel, channelOf(current)},
		{fieldModel, textValue(item.answer.Model)},
		{fieldTokens, item.answer.TotalTokens},
		{fieldStatus, statusOf(item.answer)},
	}

	if len(minuteRecords) > 0 {
		values = append(values, fieldValue{fieldMinutes, minuteRecords})
	}

	fields := toFields(values)

	record := feishu.BaseRecord{
		TableID:     current.BitableQATable,
		ClientToken: feishu.ClientToken("answer", current.BitableToken, strconv.FormatInt(item.answer.ID, 10)),
		Fields:      fields,
	}

	recordID, err := w.feishu.CreateBaseRecord(ctx, accessToken, current.BitableToken, record)
	if err != nil {
		return "", fmt.Errorf("写入问答失败: %w", err)
	}

	return recordID, nil
}

// minuteFields 是妙记表里一条记录的取值
func minuteFields(source store.ChatSource) map[string]any {
	values := []fieldValue{
		{fieldTitle, source.Title},
		{fieldOwner, textValue(source.Owner)},
	}

	if moment, err := time.Parse(time.RFC3339, source.MinuteCreatedAt); err == nil {
		values = append(values, fieldValue{fieldMinuteAt, moment.UnixMilli()})
	}

	if source.DurationSeconds > 0 {
		values = append(values, fieldValue{fieldDuration, summary.FormatDuration(time.Duration(source.DurationSeconds) * time.Second)})
	}

	if source.DocURL != "" {
		values = append(values, fieldValue{fieldDoc, feishu.URLValue(fieldDoc, source.DocURL)})
	}

	if source.MinuteURL != "" {
		values = append(values, fieldValue{fieldMinuteURL, feishu.URLValue(fieldMinuteURL, source.MinuteURL)})
	}

	return toFields(values)
}

// fieldValue 是记录里一个字段的取值，value 为 nil 时不写这个字段
type fieldValue struct {
	name  string
	value any
}

// toFields 把字段取值整理成接口要的结构，多维表格按字段名写入
func toFields(values []fieldValue) map[string]any {
	fields := make(map[string]any, len(values))

	for _, item := range values {
		if item.value != nil {
			fields[item.name] = item.value
		}
	}

	return fields
}

// textValue 返回文本字段的取值，空文本返回 nil 表示不写
func textValue(content string) any {
	if content = strings.TrimSpace(content); content != "" {
		return content
	}

	return nil
}

// plainAnswer 把回答写成纯文本：引用标记换成〔序号〕，去掉加粗与标题符号，没有正文时写出错原因
func plainAnswer(answer *store.ChatMessage) string {
	text := chat.ReplaceCitations(answer.Content, func(index int, _ string) string {
		return "〔" + strconv.Itoa(index) + "〕"
	})

	text = headingMarks.ReplaceAllString(boldMarks.ReplaceAllString(text, "$1"), "")

	if strings.TrimSpace(text) == "" {
		return answer.Error
	}

	return text
}

// sourceDetail 写出每条引用的标题、时间与链接
func sourceDetail(sources []store.ChatSource) string {
	blocks := make([]string, 0, len(sources))

	for _, source := range sources {
		lines := []string{"〔" + strconv.Itoa(source.Index) + "〕" + source.Title}

		if moment, err := time.Parse(time.RFC3339, source.MinuteCreatedAt); err == nil {
			lines[0] += " · " + moment.Local().Format("2006-01-02 15:04")
		}

		if source.DocURL != "" {
			lines = append(lines, "会议纪要："+source.DocURL)
		}

		if source.MinuteURL != "" {
			lines = append(lines, "妙记："+source.MinuteURL)
		}

		for _, moment := range source.Moments {
			lines = append(lines, moment.Label+"："+moment.URL)
		}

		blocks = append(blocks, strings.Join(lines, "\n"))
	}

	return strings.Join(blocks, "\n\n")
}

// scopeOf 返回提问时的检索范围，早期没有记下的按对话当前的范围
func scopeOf(owner *store.User, current *store.Chat, question *store.ChatMessage) string {
	if question.Scope != "" {
		return question.Scope
	}

	return chat.Describe(owner, current.Context)
}

func channelOf(current *store.Chat) string {
	if current.Source == store.ChatSourceBot {
		return channelBot
	}

	return channelWeb
}

func statusOf(answer *store.ChatMessage) string {
	switch answer.Error {
	case "":
		return statusAnswered
	case chat.Interrupted:
		return statusInterrupted
	default:
		return statusFailed
	}
}
