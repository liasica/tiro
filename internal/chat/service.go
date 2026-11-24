// Package chat 实现基于妙记与会议纪要的对话：模型按需调用检索工具，回答附带引用来源，网页与机器人共用
package chat

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/liasica/tiro/internal/llm"
	"github.com/liasica/tiro/internal/store"
)

//go:embed prompt.md
var promptTemplate string

const (
	// 一个问题最多调用几轮工具，用完后要求模型直接作答
	maxToolRounds = 8

	// 每次提问带上的历史消息条数
	historyLimit = 20

	// 对话标题取问题的前几个字
	titleRunes = 30
)

// Interrupted 是回答被中断时消息里记下的原因
const Interrupted = "回答已中断"

var (
	// ErrBusy 表示对话正在回答上一个问题
	ErrBusy = errors.New("这个对话正在回答中")
	// ErrEnded 表示对话已结束，不能继续提问
	ErrEnded = errors.New("对话已结束")
	// ErrNoModel 表示没有可用的对话模型
	ErrNoModel = errors.New("对话模型尚未配置")
)

// Transcripts 读取妙记的逐字稿
type Transcripts interface {
	Transcript(ctx context.Context, record *store.Minute) (string, error)
}

// Service 负责对话的提问与回答
type Service struct {
	store       *store.Store
	transcripts Transcripts
	// busy 记录正在回答的对话
	busy sync.Map
	// answered 在有待整理成文档的回答保存后通知，传的是回答的消息 ID
	answered chan int64
}

// New 创建 Service
func New(database *store.Store, transcripts Transcripts) *Service {
	return &Service{
		store:       database,
		transcripts: transcripts,
		answered:    make(chan int64, 64),
	}
}

// Answered 返回有待整理成文档的回答保存时收到消息 ID 的通道
func (s *Service) Answered() <-chan int64 {
	return s.answered
}

// Lock 占用对话，同一对话同时只回答一个问题；已被占用时返回 false
func (s *Service) Lock(chatID int64) (func(), bool) {
	if _, loaded := s.busy.LoadOrStore(chatID, struct{}{}); loaded {
		return nil, false
	}

	return func() { s.busy.Delete(chatID) }, true
}

// Callbacks 接收回答过程中的事件，各项都可以为 nil
type Callbacks struct {
	// Question 在问题保存后调用
	Question func(*store.ChatMessage)
	Step     func(store.ChatStep)
	// Delta 交出模型原始输出的增量，出处标记还没有改写
	Delta func(string)
	// Reset 表示此前的增量作废，模型转去调用工具了
	Reset func()
}

// Request 是一次提问
type Request struct {
	User     *store.User
	Chat     *store.Chat
	Question string
	Model    store.ChatModel
}

// Ask 在对话里提问：保存问题，检索并回答，保存回答后返回，调用方需先用 Lock 占用对话
//
// 回答出错时照常保存并返回，原因写在消息的 Error 里；返回的 error 只表示读写数据库失败
func (s *Service) Ask(ctx context.Context, request Request, callbacks Callbacks) (*store.ChatMessage, error) {
	chat := request.Chat

	history, err := s.store.ChatMessages(ctx, chat.ID)
	if err != nil {
		return nil, err
	}

	question := &store.ChatMessage{
		ChatID:  chat.ID,
		Role:    store.ChatRoleUser,
		Content: request.Question,
		Scope:   Describe(request.User, chat.Context),
	}
	if err = s.store.InsertChatMessage(ctx, question); err != nil {
		return nil, err
	}

	if callbacks.Question != nil {
		callbacks.Question(question)
	}

	if chat.Title == "" {
		chat.Title = TitleFrom(request.Question)
	}

	chat.ModelID = request.Model.ID

	if err = s.store.UpdateChat(ctx, chat); err != nil {
		return nil, err
	}

	r := &run{
		service:     s,
		store:       s.store,
		user:        request.User,
		scope:       newScope(request.User, chat.Context),
		callbacks:   callbacks,
		minutes:     make(map[int64]*store.Minute),
		transcripts: make(map[int64][]rune),
		model: llm.Config{
			BaseURL: request.Model.BaseURL,
			APIKey:  request.Model.APIKey,
			Model:   request.Model.Model,
			API:     request.Model.API,
		},
	}

	answer := r.answer(ctx, chat.ID, r.session(ctx, history), request.Question)

	// 只有正常完成且引用了妙记的回答才整理成文档
	if answer.Error == "" && len(answer.Sources) > 0 {
		answer.Result.State = store.ResultPending
	}

	// 进程退出或用户中断时 ctx 已取消，回答照样要存下来
	saveCtx := context.WithoutCancel(ctx)

	if err = s.store.InsertChatMessage(saveCtx, answer); err != nil {
		return nil, err
	}

	if err = s.store.UpdateChat(saveCtx, chat); err != nil {
		return nil, err
	}

	if answer.Result.State == store.ResultPending {
		select {
		case s.answered <- answer.ID:
		default:
		}
	}

	return answer, nil
}

// TitleFrom 用问题的开头作对话标题
func TitleFrom(question string) string {
	title := strings.Join(strings.Fields(question), " ")
	if utf8.RuneCountInString(title) > titleRunes {
		title = string([]rune(title)[:titleRunes]) + "…"
	}

	return title
}

// scope 是一次提问可检索的妙记范围，各项为零值表示不限
type scope struct {
	// userID 为 0 表示全部成员
	userID int64
	ids    []int64
	since  time.Time
	until  time.Time
}

func newScope(user *store.User, chatContext store.ChatContext) scope {
	result := scope{userID: user.ID, ids: chatContext.MinuteIDs}

	if chatContext.Scope == store.ChatScopeAll && user.Role == store.RoleAdmin {
		result.userID = 0
	}

	result.since, result.until, _ = parseRange(chatContext.StartDate, chatContext.EndDate)

	return result
}

// run 是一次提问的执行过程
type run struct {
	service   *Service
	store     *store.Store
	user      *store.User
	scope     scope
	model     llm.Config
	callbacks Callbacks
	steps     []store.ChatStep
	usage     llm.Usage

	// minutes 缓存读过的妙记，transcripts 缓存读过的逐字稿
	minutes     map[int64]*store.Minute
	transcripts map[int64][]rune
}

// answer 检索并回答，组装成待保存的助手消息，出错原因写进 Error
func (r *run) answer(ctx context.Context, chatID int64, session *llm.Session, question string) *store.ChatMessage {
	content, err := r.execute(ctx, session, question)

	answer := &store.ChatMessage{
		ChatID:           chatID,
		Role:             store.ChatRoleAssistant,
		Steps:            r.steps,
		Model:            r.usage.Model,
		PromptTokens:     r.usage.PromptTokens,
		CompletionTokens: r.usage.CompletionTokens,
		TotalTokens:      r.usage.TotalTokens,
		DurationMillis:   r.usage.Duration.Milliseconds(),
	}

	answer.Content, answer.Sources = r.cite(context.WithoutCancel(ctx), strings.TrimSpace(content))

	switch {
	case err == nil:
	case ctx.Err() != nil:
		answer.Error = Interrupted
	default:
		answer.Error = err.Error()

		slog.Warn("对话回答出错", "chat", chatID, "error", err)
	}

	return answer
}

// execute 循环调用模型，执行它要求的工具，直到它给出回答
//
// 出错时返回已经生成的部分回答
func (r *run) execute(ctx context.Context, session *llm.Session, question string) (string, error) {
	input := []llm.Message{{Role: llm.RoleUser, Content: question}}

	for range maxToolRounds {
		reply, usage, err := session.Send(ctx, input, llm.Turn{Tools: tools}, r.callbacks.Delta)

		r.usage.Add(usage)

		if err != nil {
			if len(reply.ToolCalls) > 0 {
				return "", err
			}

			return reply.Content, err
		}

		if len(reply.ToolCalls) == 0 {
			return reply.Content, nil
		}

		if reply.Content != "" && r.callbacks.Reset != nil {
			r.callbacks.Reset()
		}

		input = make([]llm.Message, 0, len(reply.ToolCalls))

		for _, call := range reply.ToolCalls {
			input = append(input, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    r.callTool(ctx, call),
			})
		}
	}

	// 工具轮次用完，最后一轮禁止调用工具；历史里有工具调用，部分接口要求照常带上工具定义
	input = append(input, llm.Message{Role: llm.RoleUser, Content: "检索次数已用完，根据已经获得的信息直接作答"})

	reply, usage, err := session.Send(ctx, input, llm.Turn{Tools: tools, ToolChoice: llm.ToolChoiceNone}, r.callbacks.Delta)

	r.usage.Add(usage)

	return reply.Content, err
}

// session 用系统提示词与历史消息开始一段模型对话
func (r *run) session(ctx context.Context, history []*store.ChatMessage) *llm.Session {
	var messages []llm.Message

	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}

	for _, message := range history {
		content := message.Content
		if message.Role == store.ChatRoleAssistant {
			content = modelText(message)
		}

		if strings.TrimSpace(content) == "" {
			continue
		}

		messages = append(messages, llm.Message{Role: message.Role, Content: content})
	}

	return llm.NewSession(r.model, r.systemPrompt(ctx), messages)
}

// systemPrompt 把当前时间与检索范围填进提示词
func (r *run) systemPrompt(ctx context.Context) string {
	now := time.Now()

	nowText := formatMoment(now) + "（GMT" + now.Format("-07") + "）"

	return strings.NewReplacer("{{now}}", nowText, "{{scope}}", r.describeScope(ctx)).Replace(strings.TrimSpace(promptTemplate))
}

// describeScope 写出检索范围；用户选定了妙记时直接列出，模型可以跳过检索直接阅读
func (r *run) describeScope(ctx context.Context) string {
	lines := []string{"- 用户本人的妙记"}

	if r.scope.userID == 0 {
		lines = []string{"- 全部成员的妙记"}
	}

	if !r.scope.since.IsZero() || !r.scope.until.IsZero() {
		lines = append(lines, "- 时间限定为"+strings.TrimSuffix(rangeText(r.scope.since, r.scope.until), "的妙记"))
	}

	if len(r.scope.ids) == 0 {
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "- 限定为用户选定的以下妙记：")

	records, _, err := r.store.SearchMinutes(ctx, store.MinuteSearch{
		UserID: r.scope.userID,
		IDs:    r.scope.ids,
		Limit:  len(r.scope.ids),
	})
	if err != nil {
		slog.Warn("读取选定的妙记失败", "error", err)
	}

	for _, record := range records {
		r.remember(record)

		lines = append(lines, "  - "+r.describe(record))
	}

	return strings.Join(lines, "\n")
}

// addStep 记下一个检索步骤并通知调用方
func (r *run) addStep(kind, text string) {
	step := store.ChatStep{Kind: kind, Text: text}

	r.steps = append(r.steps, step)

	if r.callbacks.Step != nil {
		r.callbacks.Step(step)
	}
}

// remember 缓存一条读到的妙记
func (r *run) remember(record *store.Minute) {
	r.minutes[record.ID] = record
}

// visibleMinute 返回当前用户能看到的妙记，找不到或无权查看时返回 nil
func (r *run) visibleMinute(ctx context.Context, id int64) *store.Minute {
	record, ok := r.minutes[id]
	if !ok {
		var err error

		record, err = r.store.Minute(ctx, id)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				slog.Warn("读取妙记失败", "id", id, "error", err)
			}

			return nil
		}

		r.remember(record)
	}

	if record.UserID != r.user.ID && r.user.Role != store.RoleAdmin {
		return nil
	}

	return record
}

// scopedMinute 返回检索范围内的妙记，不在范围内时返回 nil
func (r *run) scopedMinute(ctx context.Context, id int64) *store.Minute {
	record := r.visibleMinute(ctx, id)
	if record == nil {
		return nil
	}

	switch {
	case r.scope.userID != 0 && record.UserID != r.scope.userID:
		return nil
	case len(r.scope.ids) > 0 && !slices.Contains(r.scope.ids, id):
		return nil
	case !r.scope.since.IsZero() && record.MinuteCreatedAt.Before(r.scope.since):
		return nil
	case !r.scope.until.IsZero() && !record.MinuteCreatedAt.Before(r.scope.until):
		return nil
	}

	return record
}

// Describe 写出对话检索范围的简短说明，用于结果文档，例如「11-10 至 11-15 · 已选 3 条妙记」
func Describe(user *store.User, chatContext store.ChatContext) string {
	current := newScope(user, chatContext)

	var parts []string

	if current.userID == 0 {
		parts = append(parts, "全部成员")
	}

	if !current.since.IsZero() || !current.until.IsZero() {
		parts = append(parts, strings.TrimSpace(strings.TrimSuffix(rangeText(current.since, current.until), "的妙记")))
	}

	if len(current.ids) > 0 {
		parts = append(parts, "已选 "+strconv.Itoa(len(current.ids))+" 条妙记")
	}

	if len(parts) == 0 {
		return "全部妙记"
	}

	return strings.Join(parts, " · ")
}
