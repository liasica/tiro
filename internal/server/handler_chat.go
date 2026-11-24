package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/liasica/tiro/internal/chat"
	"github.com/liasica/tiro/internal/store"
)

const (
	// 对话列表最多返回的条数
	maxChatList = 100

	// 问题的字数上限
	maxQuestionRunes = 4000

	// 手动选择的妙记条数上限
	maxContextMinutes = 20

	// 回答流没有输出时隔这么久发一次保活注释，避免反向代理按读超时断开
	keepAliveInterval = 15 * time.Second
)

// chatView 是返回给前端的对话概要
type chatView struct {
	ID         int64   `json:"id"`
	Title      string  `json:"title"`
	Source     string  `json:"source"`
	Ended      bool    `json:"ended"`
	BitableURL string  `json:"bitable_url"`
	CreatedAt  *string `json:"created_at"`
	UpdatedAt  *string `json:"updated_at"`
}

// chatMessageView 是返回给前端的一条消息
type chatMessageView struct {
	ID               int64              `json:"id"`
	Role             string             `json:"role"`
	Content          string             `json:"content"`
	Steps            []store.ChatStep   `json:"steps"`
	Sources          []store.ChatSource `json:"sources"`
	Model            string             `json:"model"`
	PromptTokens     int64              `json:"prompt_tokens"`
	CompletionTokens int64              `json:"completion_tokens"`
	TotalTokens      int64              `json:"total_tokens"`
	DurationMillis   int64              `json:"duration_ms"`
	Error            string             `json:"error"`
	RecordStatus     string             `json:"record_status"`
	RecordError      string             `json:"record_error"`
	CreatedAt        *string            `json:"created_at"`
}

// contextMinuteView 是对话里手动选择的一条妙记
type contextMinuteView struct {
	ID              int64   `json:"id"`
	Title           string  `json:"title"`
	MinuteCreatedAt *string `json:"minute_created_at"`
}

func newChatView(current *store.Chat) chatView {
	return chatView{
		ID:         current.ID,
		Title:      current.Title,
		Source:     current.Source,
		Ended:      current.Ended(),
		BitableURL: current.BitableURL,
		CreatedAt:  timeText(current.CreatedAt),
		UpdatedAt:  timeText(current.UpdatedAt),
	}
}

func newChatMessageView(message *store.ChatMessage) chatMessageView {
	view := chatMessageView{
		ID:               message.ID,
		Role:             message.Role,
		Content:          message.Content,
		Steps:            message.Steps,
		Sources:          message.Sources,
		Model:            message.Model,
		PromptTokens:     message.PromptTokens,
		CompletionTokens: message.CompletionTokens,
		TotalTokens:      message.TotalTokens,
		DurationMillis:   message.DurationMillis,
		Error:            message.Error,
		RecordStatus:     message.RecordStatus(),
		RecordError:      message.RecordError,
		CreatedAt:        timeText(message.CreatedAt),
	}

	if view.Steps == nil {
		view.Steps = []store.ChatStep{}
	}

	if view.Sources == nil {
		view.Sources = []store.ChatSource{}
	}

	return view
}

// handleChatModels 返回对话可选的模型与默认模型
func (s *Server) handleChatModels(w http.ResponseWriter, r *http.Request, _ *store.User) {
	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	type option struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Model string `json:"model"`
	}

	items := make([]option, 0)

	for _, model := range config.ChatModelOptions() {
		items = append(items, option{ID: model.ID, Name: model.Name, Model: model.Model})
	}

	defaultModel, _ := config.ChatModel("")

	writeJSON(w, http.StatusOK, map[string]any{"items": items, "default_id": defaultModel.ID})
}

// handleChatList 返回当前用户的对话
func (s *Server) handleChatList(w http.ResponseWriter, r *http.Request, user *store.User) {
	chats, err := s.store.Chats(r.Context(), user.ID, maxChatList)
	if err != nil {
		writeInternalError(w, err)

		return
	}

	items := make([]chatView, 0, len(chats))
	for _, current := range chats {
		items = append(items, newChatView(current))
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// loadChat 读取路径里指定的对话，只有本人可以访问，失败时已写出响应并返回 nil
func (s *Server) loadChat(w http.ResponseWriter, r *http.Request, user *store.User, id int64) *store.Chat {
	current, err := s.store.Chat(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && current.UserID != user.ID) {
		writeError(w, http.StatusNotFound, "对话不存在")

		return nil
	}

	if err != nil {
		writeInternalError(w, err)

		return nil
	}

	return current
}

// pathChat 按路径参数读取对话
func (s *Server) pathChat(w http.ResponseWriter, r *http.Request, user *store.User) *store.Chat {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "对话 ID 无效")

		return nil
	}

	return s.loadChat(w, r, user, id)
}

// handleChatDetail 返回对话、检索范围与全部消息
func (s *Server) handleChatDetail(w http.ResponseWriter, r *http.Request, user *store.User) {
	current := s.pathChat(w, r, user)
	if current == nil {
		return
	}

	messages, err := s.store.ChatMessages(r.Context(), current.ID)
	if err != nil {
		writeInternalError(w, err)

		return
	}

	messageViews := make([]chatMessageView, 0, len(messages))
	for _, message := range messages {
		messageViews = append(messageViews, newChatMessageView(message))
	}

	contextMinutes := make([]contextMinuteView, 0, len(current.Context.MinuteIDs))

	if len(current.Context.MinuteIDs) > 0 {
		var records []*store.Minute

		records, _, err = s.store.SearchMinutes(r.Context(), store.MinuteSearch{
			IDs:   current.Context.MinuteIDs,
			Limit: len(current.Context.MinuteIDs),
		})
		if err != nil {
			writeInternalError(w, err)

			return
		}

		for _, record := range records {
			if record.UserID != user.ID && user.Role != store.RoleAdmin {
				continue
			}

			contextMinutes = append(contextMinutes, contextMinuteView{
				ID:              record.ID,
				Title:           record.Title,
				MinuteCreatedAt: timeText(record.MinuteCreatedAt),
			})
		}
	}

	writeJSON(w, http.StatusOK, struct {
		chatView

		Context        store.ChatContext   `json:"context"`
		ContextMinutes []contextMinuteView `json:"context_minutes"`
		ModelID        string              `json:"model_id"`
		Messages       []chatMessageView   `json:"messages"`
	}{
		chatView:       newChatView(current),
		Context:        viewContext(current.Context),
		ContextMinutes: contextMinutes,
		ModelID:        current.ModelID,
		Messages:       messageViews,
	})
}

// viewContext 补齐检索范围的默认值，空数组不输出为 null
func viewContext(chatContext store.ChatContext) store.ChatContext {
	if chatContext.Scope != store.ChatScopeAll {
		chatContext.Scope = "mine"
	}

	if chatContext.MinuteIDs == nil {
		chatContext.MinuteIDs = []int64{}
	}

	return chatContext
}

// handleChatDelete 删除对话，多维表格保留在用户云空间里
func (s *Server) handleChatDelete(w http.ResponseWriter, r *http.Request, user *store.User) {
	current := s.pathChat(w, r, user)
	if current == nil {
		return
	}

	if err := s.store.DeleteChat(r.Context(), current.ID); err != nil {
		writeInternalError(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleChatEnd 结束对话
func (s *Server) handleChatEnd(w http.ResponseWriter, r *http.Request, user *store.User) {
	current := s.pathChat(w, r, user)
	if current == nil {
		return
	}

	if err := s.store.EndChat(r.Context(), current.ID); err != nil {
		writeInternalError(w, err)

		return
	}

	if current = s.loadChat(w, r, user, current.ID); current == nil {
		return
	}

	writeJSON(w, http.StatusOK, newChatView(current))
}

// askInput 是提问的请求体
type askInput struct {
	ChatID   *int64            `json:"chat_id"`
	Question string            `json:"question"`
	Context  store.ChatContext `json:"context"`
	ModelID  string            `json:"model_id"`
}

// handleChatAsk 提问并以 SSE 推送检索步骤、回答增量与最终回答
func (s *Server) handleChatAsk(w http.ResponseWriter, r *http.Request, user *store.User) {
	var input askInput
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	question := strings.TrimSpace(input.Question)
	if question == "" || utf8.RuneCountInString(question) > maxQuestionRunes {
		writeError(w, http.StatusBadRequest, "问题不能为空，且不超过 "+strconv.Itoa(maxQuestionRunes)+" 字")

		return
	}

	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	model, ok := config.ChatModel(input.ModelID)
	if !ok {
		writeError(w, http.StatusConflict, chat.ErrNoModel.Error())

		return
	}

	current := s.askChat(w, r, user, input)
	if current == nil {
		return
	}

	unlock, locked := s.chat.Lock(current.ID)
	if !locked {
		writeError(w, http.StatusConflict, chat.ErrBusy.Error())

		return
	}

	defer unlock()

	current.Context = cleanContext(user, input.Context)

	stream := newEventStream(w)
	defer stream.keepAlive()()

	stream.send("chat", newChatView(current))

	var answer *store.ChatMessage

	request := chat.Request{User: user, Chat: current, Question: question, Model: model}

	answer, err = s.chat.Ask(r.Context(), request, chat.Callbacks{
		Question: func(message *store.ChatMessage) { stream.send("question", newChatMessageView(message)) },
		Step:     func(step store.ChatStep) { stream.send("step", step) },
		Delta:    func(text string) { stream.send("delta", map[string]string{"text": text}) },
		Reset:    func() { stream.send("reset", struct{}{}) },
	})
	if err != nil {
		slog.Error("保存对话失败", "chat", current.ID, "error", err)

		answer = &store.ChatMessage{Role: store.ChatRoleAssistant, Error: "回答保存失败"}
	}

	stream.send("answer", newChatMessageView(answer))
}

// askChat 取出要提问的对话，没有给出对话时新建，失败时已写出响应并返回 nil
func (s *Server) askChat(w http.ResponseWriter, r *http.Request, user *store.User, input askInput) *store.Chat {
	if input.ChatID != nil {
		current := s.loadChat(w, r, user, *input.ChatID)
		if current != nil && current.Ended() {
			writeError(w, http.StatusConflict, chat.ErrEnded.Error())

			return nil
		}

		return current
	}

	current := &store.Chat{
		UserID:  user.ID,
		Source:  store.ChatSourceWeb,
		Title:   chat.TitleFrom(input.Question),
		ModelID: input.ModelID,
	}

	if err := s.store.CreateChat(r.Context(), current); err != nil {
		writeInternalError(w, err)

		return nil
	}

	return current
}

// cleanContext 校验检索范围：成员只能检索自己的妙记，日期不合法时不限，妙记去重并限制条数
func cleanContext(user *store.User, input store.ChatContext) store.ChatContext {
	result := store.ChatContext{Scope: "mine", MinuteIDs: []int64{}}

	if input.Scope == store.ChatScopeAll && user.Role == store.RoleAdmin {
		result.Scope = store.ChatScopeAll
	}

	if _, err := time.Parse(time.DateOnly, input.StartDate); err == nil {
		result.StartDate = input.StartDate
	}

	if _, err := time.Parse(time.DateOnly, input.EndDate); err == nil {
		result.EndDate = input.EndDate
	}

	for _, id := range input.MinuteIDs {
		if id > 0 && !slices.Contains(result.MinuteIDs, id) && len(result.MinuteIDs) < maxContextMinutes {
			result.MinuteIDs = append(result.MinuteIDs, id)
		}
	}

	return result
}

// eventStream 以 SSE 格式逐条推送事件，可以从多个协程写入
type eventStream struct {
	mutex      sync.Mutex
	w          http.ResponseWriter
	controller *http.ResponseController
}

func newEventStream(w http.ResponseWriter) *eventStream {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	// 经 nginx 反向代理时关闭缓冲，增量才能及时送达
	header.Set("X-Accel-Buffering", "no")

	w.WriteHeader(http.StatusOK)

	return &eventStream{w: w, controller: http.NewResponseController(w)}
}

// send 推送一个事件，客户端断开后写入失败时直接忽略
func (e *eventStream) send(name string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("编码推送事件失败", "event", name, "error", err)

		return
	}

	e.write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, encoded))
}

// write 写出一段内容并立即送出，客户端断开后写入失败时直接忽略
func (e *eventStream) write(text string) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	if _, err := io.WriteString(e.w, text); err != nil {
		return
	}

	_ = e.controller.Flush()
}

// keepAlive 定时发送 SSE 注释保持连接，返回停止函数
func (e *eventStream) keepAlive() func() {
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(keepAliveInterval)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				e.write(": ping\n\n")
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}
