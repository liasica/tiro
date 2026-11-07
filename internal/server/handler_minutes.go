package server

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/worker"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100

	scopeAll = "all"
)

// 妙记 token 由小写字母与数字组成
var minuteTokenPattern = regexp.MustCompile(`^[a-z0-9]{16,40}$`)

// ownerView 是记录所有者的展示信息
type ownerView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// minuteView 是返回给前端的一条记录
type minuteView struct {
	ID              int64     `json:"id"`
	MinuteToken     string    `json:"minute_token"`
	MinuteURL       string    `json:"minute_url"`
	Title           string    `json:"title"`
	Owner           ownerView `json:"owner"`
	MinuteCreatedAt *string   `json:"minute_created_at"`
	DurationSeconds int64     `json:"duration_seconds"`
	Status          string    `json:"status"`
	Reason          string    `json:"reason"`
	Attempts        int       `json:"attempts"`
	DocURL          string    `json:"doc_url"`
	TotalTokens     int64     `json:"total_tokens"`
	CreatedAt       *string   `json:"created_at"`
	FinishedAt      *string   `json:"finished_at"`
}

// llmCallView 是返回给前端的一次模型调用
type llmCallView struct {
	ID               int64   `json:"id"`
	Model            string  `json:"model"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	DurationMillis   int64   `json:"duration_ms"`
	Error            string  `json:"error"`
	CreatedAt        *string `json:"created_at"`
}

func newLLMCallView(call *store.LLMCall) llmCallView {
	return llmCallView{
		ID:               call.ID,
		Model:            call.Model,
		PromptTokens:     call.PromptTokens,
		CachedTokens:     call.CachedTokens,
		CompletionTokens: call.CompletionTokens,
		ReasoningTokens:  call.ReasoningTokens,
		TotalTokens:      call.TotalTokens,
		DurationMillis:   call.DurationMillis,
		Error:            call.Error,
		CreatedAt:        timeText(call.CreatedAt),
	}
}

func newMinuteView(record *store.Minute) minuteView {
	return minuteView{
		ID:              record.ID,
		MinuteToken:     record.MinuteToken,
		MinuteURL:       record.MinuteURL,
		Title:           record.Title,
		Owner:           ownerView{ID: record.UserID, Name: record.OwnerName, AvatarURL: record.OwnerAvatarURL},
		MinuteCreatedAt: timeText(record.MinuteCreatedAt),
		DurationSeconds: record.DurationSeconds,
		Status:          record.Status,
		Reason:          record.Reason,
		Attempts:        record.Attempts,
		DocURL:          record.DocURL,
		TotalTokens:     record.TotalTokens,
		CreatedAt:       timeText(record.CreatedAt),
		FinishedAt:      timeText(record.FinishedAt),
	}
}

// scopedUserID 按 scope 参数决定查询范围，只有管理员可以查看全部成员的记录
func scopedUserID(r *http.Request, user *store.User) int64 {
	if r.URL.Query().Get("scope") == scopeAll && user.Role == store.RoleAdmin {
		return 0
	}

	return user.ID
}

// handleMinuteList 分页返回记录
func (s *Server) handleMinuteList(w http.ResponseWriter, r *http.Request, user *store.User) {
	query := r.URL.Query()

	page := positiveInt(query.Get("page"), 1)
	pageSize := min(positiveInt(query.Get("page_size"), defaultPageSize), maxPageSize)

	records, total, err := s.store.ListMinutes(r.Context(), store.MinuteFilter{
		UserID: scopedUserID(r, user),
		Status: query.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	})
	if err != nil {
		writeInternalError(w, err)

		return
	}

	items := make([]minuteView, 0, len(records))
	for _, record := range records {
		items = append(items, newMinuteView(record))
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// positiveInt 解析正整数，不是正整数时返回 fallback
func positiveInt(text string, fallback int) int {
	value, err := strconv.Atoi(text)
	if err != nil || value <= 0 {
		return fallback
	}

	return value
}

// loadMinute 读取路径里指定的记录并校验访问权限，失败时已写出响应并返回 nil
func (s *Server) loadMinute(w http.ResponseWriter, r *http.Request, user *store.User) *store.Minute {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "记录 ID 无效")

		return nil
	}

	var record *store.Minute

	record, err = s.store.Minute(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && record.UserID != user.ID && user.Role != store.RoleAdmin) {
		writeError(w, http.StatusNotFound, "记录不存在")

		return nil
	}

	if err != nil {
		writeInternalError(w, err)

		return nil
	}

	return record
}

// handleMinuteDetail 返回一条记录、纪要正文与历次模型调用
func (s *Server) handleMinuteDetail(w http.ResponseWriter, r *http.Request, user *store.User) {
	record := s.loadMinute(w, r, user)
	if record == nil {
		return
	}

	calls, err := s.store.LLMCalls(r.Context(), record.ID)
	if err != nil {
		writeInternalError(w, err)

		return
	}

	callViews := make([]llmCallView, 0, len(calls))
	for _, call := range calls {
		callViews = append(callViews, newLLMCallView(call))
	}

	writeJSON(w, http.StatusOK, struct {
		minuteView

		SummaryMarkdown string        `json:"summary_markdown"`
		LLMCalls        []llmCallView `json:"llm_calls"`
	}{minuteView: newMinuteView(record), SummaryMarkdown: record.SummaryMarkdown, LLMCalls: callViews})
}

// handleMinuteGenerate 把一条记录重新排队生成
func (s *Server) handleMinuteGenerate(w http.ResponseWriter, r *http.Request, user *store.User) {
	record := s.loadMinute(w, r, user)
	if record == nil {
		return
	}

	requeued, err := s.store.RequeueMinute(r.Context(), record.ID)
	if err != nil {
		writeInternalError(w, err)

		return
	}

	if !requeued {
		writeError(w, http.StatusConflict, worker.ErrBusy.Error())

		return
	}

	s.worker.Wake()
	w.WriteHeader(http.StatusAccepted)
}

// handleMinuteAdd 按妙记链接或 token 手动添加一条记录
func (s *Server) handleMinuteAdd(w http.ResponseWriter, r *http.Request, user *store.User) {
	var input struct {
		URL string `json:"url"`
	}

	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	minuteToken := parseMinuteToken(input.URL)
	if minuteToken == "" {
		writeError(w, http.StatusBadRequest, "无法从输入中识别妙记，粘贴妙记链接或链接末尾的 token")

		return
	}

	record, err := s.worker.AddMinute(r.Context(), user, minuteToken)
	if errors.Is(err, worker.ErrReauthorize) {
		writeError(w, http.StatusConflict, "飞书授权已失效，重新扫码登录后再试")

		return
	}

	if errors.Is(err, worker.ErrBusy) {
		writeError(w, http.StatusConflict, err.Error())

		return
	}

	if err != nil {
		writeError(w, http.StatusBadGateway, "读取妙记失败: "+err.Error())

		return
	}

	writeJSON(w, http.StatusCreated, newMinuteView(record))
}

// parseMinuteToken 从妙记链接或裸 token 里取出 token，识别不出时返回空串
func parseMinuteToken(input string) string {
	candidate := strings.TrimSpace(input)

	if parsed, err := url.Parse(candidate); err == nil && parsed.Host != "" {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")

		candidate = segments[len(segments)-1]
	}

	if !minuteTokenPattern.MatchString(candidate) {
		return ""
	}

	return candidate
}

// handleStats 返回各状态的记录数与最近一次检查时间
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, user *store.User) {
	stats, err := s.store.MinuteStats(r.Context(), scopedUserID(r, user))
	if err != nil {
		writeInternalError(w, err)

		return
	}

	var config *store.Config

	config, err = s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":        stats.Total,
		"done":         stats.Done,
		"failed":       stats.Failed,
		"pending":      stats.Pending,
		"skipped":      stats.Skipped,
		"total_tokens": stats.TotalTokens,
		"last_sync_at": timeText(config.LastSyncAt),
	})
}
