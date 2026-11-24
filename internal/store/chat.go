package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// 对话的来源
const (
	ChatSourceWeb = "web"
	ChatSourceBot = "bot"
)

// 对话消息的角色
const (
	ChatRoleUser      = "user"
	ChatRoleAssistant = "assistant"
)

// ChatScopeAll 表示在全部成员的妙记里检索，只对管理员生效
const ChatScopeAll = "all"

// ChatContext 是对话的检索范围，各项为空表示不限
type ChatContext struct {
	Scope     string  `json:"scope"`
	MinuteIDs []int64 `json:"minute_ids"`
	// StartDate 与 EndDate 是 YYYY-MM-DD 格式的日期，含当天
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

// Chat 是一个对话
type Chat struct {
	ID        int64
	UserID    int64
	Source    string
	Title     string
	Context   ChatContext
	ModelID   string
	EndedAt   time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Ended 判断对话是否已结束
func (c *Chat) Ended() bool {
	return !c.EndedAt.IsZero()
}

// ChatStep 是回答过程中的一个检索步骤
type ChatStep struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// ChatMoment 是回答里引用到的妙记时间点
type ChatMoment struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ChatSource 是回答引用的一条妙记，Index 对应正文里的引用序号
type ChatSource struct {
	Index    int    `json:"index"`
	MinuteID int64  `json:"minute_id"`
	Title    string `json:"title"`
	Owner    string `json:"owner"`
	// MinuteCreatedAt 是 RFC 3339 格式的妙记时间，未知时为空串
	MinuteCreatedAt string       `json:"minute_created_at"`
	DurationSeconds int64        `json:"duration_seconds"`
	MinuteURL       string       `json:"minute_url"`
	DocURL          string       `json:"doc_url"`
	Moments         []ChatMoment `json:"moments"`
}

// ChatMessage 是对话里的一条消息
type ChatMessage struct {
	ID               int64
	ChatID           int64
	Role             string
	Content          string
	Steps            []ChatStep
	Sources          []ChatSource
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	DurationMillis   int64
	Error            string
	CreatedAt        time.Time
	// Scope 是用户消息提问时的检索范围说明
	Scope string

	// Result 是助手消息整理成的结果文档
	Result ChatResult
}

// 结果文档的状态，空串表示这条回答不整理成文档
const (
	ResultPending = "pending"
	ResultDone    = "done"
	ResultSkipped = "skipped"
	ResultFailed  = "failed"
)

// ChatResult 是一条回答整理成的飞书文档
type ChatResult struct {
	State string
	// Kind 是结果类型，例如 todo、summary，取值见 chat 包
	Kind  string
	Title string
	URL   string
	// Markdown 是已经生成的文档正文，建文档失败重试时直接复用
	Markdown string
	Error    string
	Attempts int
	// NextAt 是下次尝试的时间，零值表示不再自动尝试
	NextAt time.Time
}

const chatColumns = `id, user_id, source, title, context, model_id, ended_at, created_at, updated_at`

func scanChat(row scanner) (*Chat, error) {
	var (
		chat                    Chat
		scope                   string
		ended, created, updated int64
	)

	err := row.Scan(
		&chat.ID,
		&chat.UserID,
		&chat.Source,
		&chat.Title,
		&scope,
		&chat.ModelID,
		&ended,
		&created,
		&updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	// 解析不了的内容按空值处理
	_ = json.Unmarshal([]byte(scope), &chat.Context)

	chat.EndedAt = fromUnix(ended)
	chat.CreatedAt = fromUnix(created)
	chat.UpdatedAt = fromUnix(updated)

	return &chat, nil
}

// CreateChat 新建对话，写回 ID 与时间
func (s *Store) CreateChat(ctx context.Context, chat *Chat) error {
	scope, err := json.Marshal(chat.Context)
	if err != nil {
		return err
	}

	now := time.Now()

	var result sql.Result

	result, err = s.db.ExecContext(
		ctx,
		`INSERT INTO chats (user_id, source, title, context, model_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		chat.UserID,
		chat.Source,
		chat.Title,
		string(scope),
		chat.ModelID,
		now.Unix(),
		now.Unix(),
	)
	if err != nil {
		return err
	}

	if chat.ID, err = result.LastInsertId(); err != nil {
		return err
	}

	chat.CreatedAt = fromUnix(now.Unix())
	chat.UpdatedAt = chat.CreatedAt

	return nil
}

// Chat 按主键读取对话
func (s *Store) Chat(ctx context.Context, id int64) (*Chat, error) {
	return scanChat(s.db.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, id))
}

// OpenChat 返回用户某个来源下最近一个未结束的对话，没有时返回 ErrNotFound
func (s *Store) OpenChat(ctx context.Context, userID int64, source string) (*Chat, error) {
	const query = `SELECT ` + chatColumns + ` FROM chats WHERE user_id = ? AND source = ? AND ended_at = 0
		ORDER BY updated_at DESC, id DESC LIMIT 1`

	return scanChat(s.db.QueryRowContext(ctx, query, userID, source))
}

// Chats 按更新时间倒序返回用户的对话
func (s *Store) Chats(ctx context.Context, userID int64, limit int) ([]*Chat, error) {
	const query = `SELECT ` + chatColumns + ` FROM chats WHERE user_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, userID, limit)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var chats []*Chat

	for rows.Next() {
		var chat *Chat
		if chat, err = scanChat(rows); err != nil {
			return nil, err
		}

		chats = append(chats, chat)
	}

	return chats, rows.Err()
}

// UpdateChat 写回对话的标题、检索范围与模型，并把更新时间记为现在
func (s *Store) UpdateChat(ctx context.Context, chat *Chat) error {
	scope, err := json.Marshal(chat.Context)
	if err != nil {
		return err
	}

	chat.UpdatedAt = fromUnix(time.Now().Unix())

	_, err = s.db.ExecContext(
		ctx,
		`UPDATE chats SET title = ?, context = ?, model_id = ?, updated_at = ? WHERE id = ?`,
		chat.Title,
		string(scope),
		chat.ModelID,
		chat.UpdatedAt.Unix(),
		chat.ID,
	)

	return err
}

// EndChat 结束对话，已经结束的不重复记录结束时间
func (s *Store) EndChat(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET ended_at = ? WHERE id = ? AND ended_at = 0`, time.Now().Unix(), id)

	return err
}

// DeleteChat 删除对话及其全部消息
func (s *Store) DeleteChat(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id)

	return err
}

// InsertChatMessage 新增一条消息，写回 ID 与时间
func (s *Store) InsertChatMessage(ctx context.Context, message *ChatMessage) error {
	steps, err := json.Marshal(orEmpty(message.Steps))
	if err != nil {
		return err
	}

	var sources []byte

	if sources, err = json.Marshal(orEmpty(message.Sources)); err != nil {
		return err
	}

	now := time.Now().Unix()

	var result sql.Result

	result, err = s.db.ExecContext(
		ctx,
		`INSERT INTO chat_messages (chat_id, role, content, steps, sources, model, prompt_tokens, completion_tokens,
			total_tokens, duration_ms, error, created_at, scope, result_state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ChatID,
		message.Role,
		message.Content,
		string(steps),
		string(sources),
		message.Model,
		message.PromptTokens,
		message.CompletionTokens,
		message.TotalTokens,
		message.DurationMillis,
		message.Error,
		now,
		message.Scope,
		message.Result.State,
	)
	if err != nil {
		return err
	}

	if message.ID, err = result.LastInsertId(); err != nil {
		return err
	}

	message.CreatedAt = fromUnix(now)

	return nil
}

const messageColumns = `id, chat_id, role, content, steps, sources, model, prompt_tokens, completion_tokens,
	total_tokens, duration_ms, error, created_at, scope, result_state, result_kind, result_title, result_url,
	result_markdown, result_error, result_attempts, result_next_at`

func scanMessage(row scanner) (*ChatMessage, error) {
	var (
		message         ChatMessage
		steps, sources  string
		created, nextAt int64
	)

	err := row.Scan(
		&message.ID,
		&message.ChatID,
		&message.Role,
		&message.Content,
		&steps,
		&sources,
		&message.Model,
		&message.PromptTokens,
		&message.CompletionTokens,
		&message.TotalTokens,
		&message.DurationMillis,
		&message.Error,
		&created,
		&message.Scope,
		&message.Result.State,
		&message.Result.Kind,
		&message.Result.Title,
		&message.Result.URL,
		&message.Result.Markdown,
		&message.Result.Error,
		&message.Result.Attempts,
		&nextAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(steps), &message.Steps)
	_ = json.Unmarshal([]byte(sources), &message.Sources)

	message.CreatedAt = fromUnix(created)
	message.Result.NextAt = fromUnix(nextAt)

	return &message, nil
}

// ChatMessages 按先后顺序返回对话的全部消息
func (s *Store) ChatMessages(ctx context.Context, chatID int64) ([]*ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageColumns+` FROM chat_messages WHERE chat_id = ? ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var messages []*ChatMessage

	for rows.Next() {
		var message *ChatMessage
		if message, err = scanMessage(rows); err != nil {
			return nil, err
		}

		messages = append(messages, message)
	}

	return messages, rows.Err()
}

// ChatMessage 按主键读取一条消息
func (s *Store) ChatMessage(ctx context.Context, id int64) (*ChatMessage, error) {
	return scanMessage(s.db.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM chat_messages WHERE id = ?`, id))
}

// ChatQuestion 返回某条回答对应的提问，即同一对话里排在它前面的最后一条用户消息
func (s *Store) ChatQuestion(ctx context.Context, answer *ChatMessage) (*ChatMessage, error) {
	const query = `SELECT ` + messageColumns + ` FROM chat_messages
		WHERE chat_id = ? AND id < ? AND role = ? ORDER BY id DESC LIMIT 1`

	return scanMessage(s.db.QueryRowContext(ctx, query, answer.ChatID, answer.ID, ChatRoleUser))
}

// SetMessageResult 写回助手消息的结果文档
func (s *Store) SetMessageResult(ctx context.Context, id int64, result ChatResult) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE chat_messages SET result_state = ?, result_kind = ?, result_title = ?, result_url = ?, result_markdown = ?,
			result_error = ?, result_attempts = ?, result_next_at = ? WHERE id = ?`,
		result.State,
		result.Kind,
		result.Title,
		result.URL,
		result.Markdown,
		result.Error,
		result.Attempts,
		toUnix(result.NextAt),
		id,
	)

	return err
}

// DueResults 返回待整理成文档的回答：还没整理过的，以及失败后到了重试时间的，只看 since 之后的消息
func (s *Store) DueResults(ctx context.Context, since, now time.Time) ([]int64, error) {
	const query = `SELECT id FROM chat_messages
		WHERE created_at >= ? AND (result_state = ? OR (result_state = ? AND result_next_at > 0 AND result_next_at <= ?))
		ORDER BY id`

	rows, err := s.db.QueryContext(
		ctx,
		query,
		since.Unix(),
		ResultPending,
		ResultFailed,
		now.Unix(),
	)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var ids []int64

	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	return ids, rows.Err()
}

// orEmpty 把 nil 切片换成空切片，编码成 [] 而不是 null
func orEmpty[T any](items []T) []T {
	if items == nil {
		return []T{}
	}

	return items
}

// MinuteSearch 是对话检索妙记的条件，各项为零值表示不限
type MinuteSearch struct {
	UserID int64
	IDs    []int64
	Since  time.Time
	// Until 不含当时
	Until   time.Time
	Keyword string
	Limit   int
}

// SearchMinutes 按条件检索妙记，按妙记时间倒序返回前 Limit 条与符合条件的总数
//
// 关键词匹配标题与纪要正文
func (s *Store) SearchMinutes(ctx context.Context, search MinuteSearch) ([]*Minute, int, error) {
	where := ` WHERE 1 = 1`

	var args []any

	if search.UserID != 0 {
		where += ` AND m.user_id = ?`

		args = append(args, search.UserID)
	}

	if len(search.IDs) > 0 {
		where += ` AND m.id IN (?` + strings.Repeat(",?", len(search.IDs)-1) + `)`

		for _, id := range search.IDs {
			args = append(args, id)
		}
	}

	if !search.Since.IsZero() {
		where += ` AND m.minute_created_at >= ?`

		args = append(args, search.Since.Unix())
	}

	if !search.Until.IsZero() {
		where += ` AND m.minute_created_at < ?`

		args = append(args, search.Until.Unix())
	}

	if keyword := strings.TrimSpace(search.Keyword); keyword != "" {
		where += ` AND (m.title LIKE ? ESCAPE '\' OR m.summary_markdown LIKE ? ESCAPE '\')`

		args = append(args, likePattern(keyword), likePattern(keyword))
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+minuteFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + minuteColumns + minuteFrom + where + ` ORDER BY m.minute_created_at DESC, m.id DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, append(args, search.Limit)...)
	if err != nil {
		return nil, 0, err
	}

	defer func() { _ = rows.Close() }()

	var minutes []*Minute

	for rows.Next() {
		var minute *Minute
		if minute, err = scanMinute(rows); err != nil {
			return nil, 0, err
		}

		minutes = append(minutes, minute)
	}

	return minutes, total, rows.Err()
}

// likeEscaper 转义 LIKE 里的通配符与转义符本身
var likeEscaper = strings.NewReplacer(
	`\`,
	`\\`,
	`%`,
	`\%`,
	`_`,
	`\_`,
)

// likePattern 把关键词转成 LIKE 的包含匹配
func likePattern(keyword string) string {
	return "%" + likeEscaper.Replace(keyword) + "%"
}
