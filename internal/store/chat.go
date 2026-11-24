package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
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

// Chat 是一个对话，每个对话的问答写进各自的多维表格
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

	// 以下是对话记录所在的多维表格，尚未创建时为空
	BitableToken       string
	BitableURL         string
	BitableQATable     string
	BitableMinuteTable string
	// BitableMinutes 是妙记记录 ID 到「引用妙记」表里记录 ID 的对应关系
	BitableMinutes map[int64]string
	// BitableSpareTable 是新建多维表格时自带、还没删掉的空数据表
	BitableSpareTable string
	// BitableLinked 表示问答表的「引用妙记」关联字段已经建好
	BitableLinked bool
}

// ResetBitable 清空多维表格的信息，下次写入时重新新建
func (c *Chat) ResetBitable() {
	c.BitableToken = ""
	c.BitableURL = ""
	c.BitableQATable = ""
	c.BitableMinuteTable = ""
	c.BitableMinutes = nil
	c.BitableSpareTable = ""
	c.BitableLinked = false
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

	// 以下是助手消息写入多维表格的结果，RecordID 为空表示还没写入
	RecordID          string
	RecordError       string
	RecordAttemptedAt time.Time
}

// 多维表格记录的写入状态
const (
	RecordPending = "pending"
	RecordDone    = "done"
	RecordFailed  = "failed"
)

// RecordStatus 返回助手消息写入多维表格的状态，用户消息返回空串
func (m *ChatMessage) RecordStatus() string {
	switch {
	case m.Role != ChatRoleAssistant, m.ID == 0:
		return ""
	case m.RecordID != "":
		return RecordDone
	case m.RecordError != "":
		return RecordFailed
	default:
		return RecordPending
	}
}

const chatColumns = `id, user_id, source, title, context, model_id, ended_at, created_at, updated_at,
	bitable_token, bitable_url, bitable_qa_table, bitable_minute_table, bitable_minutes, bitable_spare_table, bitable_linked`

func scanChat(row scanner) (*Chat, error) {
	var (
		chat                            Chat
		scope, minutes                  string
		ended, created, updated, linked int64
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
		&chat.BitableToken,
		&chat.BitableURL,
		&chat.BitableQATable,
		&chat.BitableMinuteTable,
		&minutes,
		&chat.BitableSpareTable,
		&linked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	// 解析不了的内容按空值处理
	_ = json.Unmarshal([]byte(scope), &chat.Context)
	chat.BitableMinutes = decodeRecordMap(minutes)
	chat.BitableLinked = linked != 0

	chat.EndedAt = fromUnix(ended)
	chat.CreatedAt = fromUnix(created)
	chat.UpdatedAt = fromUnix(updated)

	return &chat, nil
}

// decodeRecordMap 解析妙记记录 ID 到多维表格记录 ID 的对应关系
func decodeRecordMap(raw string) map[int64]string {
	var encoded map[string]string

	_ = json.Unmarshal([]byte(raw), &encoded)

	records := make(map[int64]string, len(encoded))

	for key, recordID := range encoded {
		if minuteID, err := strconv.ParseInt(key, 10, 64); err == nil {
			records[minuteID] = recordID
		}
	}

	return records
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

// SetChatBitable 写回对话记录所在的多维表格与其中的妙记记录
func (s *Store) SetChatBitable(ctx context.Context, chat *Chat) error {
	encoded := make(map[string]string, len(chat.BitableMinutes))
	for minuteID, recordID := range chat.BitableMinutes {
		encoded[strconv.FormatInt(minuteID, 10)] = recordID
	}

	minutes, err := json.Marshal(encoded)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(
		ctx,
		`UPDATE chats SET bitable_token = ?, bitable_url = ?, bitable_qa_table = ?, bitable_minute_table = ?,
			bitable_minutes = ?, bitable_spare_table = ?, bitable_linked = ? WHERE id = ?`,
		chat.BitableToken,
		chat.BitableURL,
		chat.BitableQATable,
		chat.BitableMinuteTable,
		string(minutes),
		chat.BitableSpareTable,
		chat.BitableLinked,
		chat.ID,
	)

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
			total_tokens, duration_ms, error, created_at, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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

// ChatMessages 按先后顺序返回对话的全部消息
func (s *Store) ChatMessages(ctx context.Context, chatID int64) ([]*ChatMessage, error) {
	const query = `SELECT id, chat_id, role, content, steps, sources, model, prompt_tokens, completion_tokens,
		total_tokens, duration_ms, error, created_at, record_id, record_error, record_attempted_at, scope
		FROM chat_messages WHERE chat_id = ? ORDER BY id`

	rows, err := s.db.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var messages []*ChatMessage

	for rows.Next() {
		var (
			message            ChatMessage
			steps, sources     string
			created, attempted int64
		)

		err = rows.Scan(
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
			&message.RecordID,
			&message.RecordError,
			&attempted,
			&message.Scope,
		)
		if err != nil {
			return nil, err
		}

		_ = json.Unmarshal([]byte(steps), &message.Steps)
		_ = json.Unmarshal([]byte(sources), &message.Sources)

		message.CreatedAt = fromUnix(created)
		message.RecordAttemptedAt = fromUnix(attempted)
		messages = append(messages, &message)
	}

	return messages, rows.Err()
}

// SetMessageRecord 记下助手消息写入多维表格的结果，recordID 为空时 recordErr 是失败原因
func (s *Store) SetMessageRecord(ctx context.Context, id int64, recordID, recordErr string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE chat_messages SET record_id = ?, record_error = ?, record_attempted_at = ? WHERE id = ?`,
		recordID,
		recordErr,
		time.Now().Unix(),
		id,
	)

	return err
}

// PendingRecordChats 返回有助手消息待写入多维表格的对话
//
// 只看 since 之后的消息；写入失败过的消息，上次尝试早于 retryBefore 才再次返回
func (s *Store) PendingRecordChats(ctx context.Context, since, retryBefore time.Time) ([]int64, error) {
	const query = `SELECT DISTINCT chat_id FROM chat_messages
		WHERE role = ? AND record_id = '' AND created_at >= ? AND record_attempted_at <= ? ORDER BY chat_id`

	rows, err := s.db.QueryContext(ctx, query, ChatRoleAssistant, since.Unix(), retryBefore.Unix())
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var chatIDs []int64

	for rows.Next() {
		var chatID int64
		if err = rows.Scan(&chatID); err != nil {
			return nil, err
		}

		chatIDs = append(chatIDs, chatID)
	}

	return chatIDs, rows.Err()
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
