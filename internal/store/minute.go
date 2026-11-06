package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 妙记处理记录的状态
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusDone       = "done"
	StatusFailed     = "failed"
	StatusSkipped    = "skipped"
)

// Minute 是一条妙记的处理记录
type Minute struct {
	ID              int64
	UserID          int64
	MinuteToken     string
	MinuteURL       string
	Title           string
	MinuteCreatedAt time.Time
	DurationSeconds int64
	Status          string
	Reason          string
	Attempts        int
	NextAttemptAt   time.Time
	DocURL          string
	SummaryMarkdown string
	DocTitle        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      time.Time

	// 以下字段来自关联的用户，只在读取时填充
	OwnerName      string
	OwnerAvatarURL string
}

// MinuteFilter 是列表查询条件，UserID 为 0 表示不限所有者
type MinuteFilter struct {
	UserID int64
	Status string
	Limit  int
	Offset int
}

// MinuteStats 是各状态的记录数
type MinuteStats struct {
	Total   int
	Done    int
	Failed  int
	Pending int
	Skipped int
}

const minuteColumns = `m.id, m.user_id, m.minute_token, m.minute_url, m.title, m.minute_created_at,
	m.duration_seconds, m.status, m.reason, m.attempts, m.next_attempt_at, m.doc_url, m.summary_markdown,
	m.doc_title, m.created_at, m.updated_at, m.finished_at, u.name, u.avatar_url`

const minuteFrom = ` FROM minutes m JOIN users u ON u.id = m.user_id`

func scanMinute(row scanner) (*Minute, error) {
	var (
		minute                                                 Minute
		minuteCreated, nextAttempt, created, updated, finished int64
	)

	err := row.Scan(
		&minute.ID,
		&minute.UserID,
		&minute.MinuteToken,
		&minute.MinuteURL,
		&minute.Title,
		&minuteCreated,
		&minute.DurationSeconds,
		&minute.Status,
		&minute.Reason,
		&minute.Attempts,
		&nextAttempt,
		&minute.DocURL,
		&minute.SummaryMarkdown,
		&minute.DocTitle,
		&created,
		&updated,
		&finished,
		&minute.OwnerName,
		&minute.OwnerAvatarURL,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	minute.MinuteCreatedAt = fromUnix(minuteCreated)
	minute.NextAttemptAt = fromUnix(nextAttempt)
	minute.CreatedAt = fromUnix(created)
	minute.UpdatedAt = fromUnix(updated)
	minute.FinishedAt = fromUnix(finished)

	return &minute, nil
}

// InsertMinute 新增一条记录，同一用户下 token 已存在时不做任何事并返回 false
func (s *Store) InsertMinute(ctx context.Context, minute *Minute) (bool, error) {
	now := time.Now().Unix()

	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO minutes (user_id, minute_token, minute_url, title, minute_created_at, duration_seconds,
			status, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, minute_token) DO NOTHING`,
		minute.UserID,
		minute.MinuteToken,
		minute.MinuteURL,
		minute.Title,
		toUnix(minute.MinuteCreatedAt),
		minute.DurationSeconds,
		minute.Status,
		minute.Reason,
		now,
		now,
	)

	return changed(result, err)
}

// ClaimMinute 把一条待处理记录标成处理中并把尝试次数加一
//
// 记录已不是待处理状态时返回 false，调用方应放弃这条记录
func (s *Store) ClaimMinute(ctx context.Context, id int64) (bool, error) {
	const query = `UPDATE minutes SET status = ?, reason = '', attempts = attempts + 1, updated_at = ?
		WHERE id = ? AND status = ?`

	return changed(s.db.ExecContext(
		ctx,
		query,
		StatusProcessing,
		time.Now().Unix(),
		id,
		StatusPending,
	))
}

// Minute 按主键读取记录
func (s *Store) Minute(ctx context.Context, id int64) (*Minute, error) {
	return scanMinute(s.db.QueryRowContext(ctx, `SELECT `+minuteColumns+minuteFrom+` WHERE m.id = ?`, id))
}

// MinuteByToken 按所有者与妙记 token 读取记录
func (s *Store) MinuteByToken(ctx context.Context, userID int64, minuteToken string) (*Minute, error) {
	const query = `SELECT ` + minuteColumns + minuteFrom + ` WHERE m.user_id = ? AND m.minute_token = ?`

	return scanMinute(s.db.QueryRowContext(ctx, query, userID, minuteToken))
}

// KnownMinuteTokens 返回该用户已入库的妙记 token 集合
func (s *Store) KnownMinuteTokens(ctx context.Context, userID int64) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT minute_token FROM minutes WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	known := make(map[string]struct{})

	for rows.Next() {
		var minuteToken string
		if err = rows.Scan(&minuteToken); err != nil {
			return nil, err
		}

		known[minuteToken] = struct{}{}
	}

	return known, rows.Err()
}

// ListMinutes 按条件分页读取记录，同时返回符合条件的总数
func (s *Store) ListMinutes(ctx context.Context, filter MinuteFilter) ([]*Minute, int, error) {
	where := ` WHERE 1 = 1`

	var args []any

	if filter.UserID != 0 {
		where += ` AND m.user_id = ?`

		args = append(args, filter.UserID)
	}

	if filter.Status != "" {
		where += ` AND m.status = ?`

		args = append(args, filter.Status)
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+minuteFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + minuteColumns + minuteFrom + where +
		` ORDER BY m.minute_created_at DESC, m.id DESC LIMIT ? OFFSET ?`

	rows, err := s.db.QueryContext(ctx, query, append(args, filter.Limit, filter.Offset)...)
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

// NextPendingMinute 取一条到了处理时间的待处理记录，没有时返回 ErrNotFound
//
// 只取所有者授权有效的记录，其余留在待处理状态；成员被停用只影响发现新妙记，已排队的照常处理
func (s *Store) NextPendingMinute(ctx context.Context, now time.Time) (*Minute, error) {
	const query = `SELECT ` + minuteColumns + minuteFrom + `
		WHERE m.status = ? AND m.next_attempt_at <= ? AND u.auth_status = ?
		ORDER BY m.next_attempt_at, m.id LIMIT 1`

	return scanMinute(s.db.QueryRowContext(ctx, query, StatusPending, now.Unix(), AuthActive))
}

// UpdateMinute 写回记录里会随处理过程变化的字段
func (s *Store) UpdateMinute(ctx context.Context, minute *Minute) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE minutes SET minute_url = ?, title = ?, minute_created_at = ?, duration_seconds = ?, status = ?,
			reason = ?, attempts = ?, next_attempt_at = ?, doc_url = ?, summary_markdown = ?, doc_title = ?,
			updated_at = ?, finished_at = ? WHERE id = ?`,
		minute.MinuteURL,
		minute.Title,
		toUnix(minute.MinuteCreatedAt),
		minute.DurationSeconds,
		minute.Status,
		minute.Reason,
		minute.Attempts,
		toUnix(minute.NextAttemptAt),
		minute.DocURL,
		minute.SummaryMarkdown,
		minute.DocTitle,
		time.Now().Unix(),
		toUnix(minute.FinishedAt),
		minute.ID,
	)

	return err
}

// RequeueMinute 把记录重新放回待处理队列，清空重试计数与上一次生成的结果
//
// 正在处理中的记录不能重新排队，此时返回 false
func (s *Store) RequeueMinute(ctx context.Context, id int64) (bool, error) {
	const query = `UPDATE minutes SET status = ?, reason = '', attempts = 0, next_attempt_at = 0, finished_at = 0,
		doc_url = '', summary_markdown = '', doc_title = '', updated_at = ? WHERE id = ? AND status <> ?`

	return changed(s.db.ExecContext(
		ctx,
		query,
		StatusPending,
		time.Now().Unix(),
		id,
		StatusProcessing,
	))
}

// ResetProcessing 把上次进程退出时停在处理中的记录放回待处理队列
//
// 被中断的那一次不算数，尝试次数退回一次
func (s *Store) ResetProcessing(ctx context.Context) error {
	const query = `UPDATE minutes SET status = ?, attempts = MAX(attempts - 1, 0), next_attempt_at = 0
		WHERE status = ?`

	_, err := s.db.ExecContext(ctx, query, StatusPending, StatusProcessing)

	return err
}

// MinuteStats 统计各状态的记录数，userID 为 0 表示全部用户
func (s *Store) MinuteStats(ctx context.Context, userID int64) (MinuteStats, error) {
	query := `SELECT status, COUNT(*) FROM minutes`

	var args []any

	if userID != 0 {
		query += ` WHERE user_id = ?`

		args = append(args, userID)
	}

	var stats MinuteStats

	rows, err := s.db.QueryContext(ctx, query+` GROUP BY status`, args...)
	if err != nil {
		return stats, err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			status string
			count  int
		)

		if err = rows.Scan(&status, &count); err != nil {
			return stats, err
		}

		stats.Total += count

		switch status {
		case StatusDone:
			stats.Done = count
		case StatusFailed:
			stats.Failed = count
		case StatusSkipped:
			stats.Skipped = count
		case StatusPending, StatusProcessing:
			stats.Pending += count
		}
	}

	return stats, rows.Err()
}

// CountMinutesByUser 返回每个用户的记录数
func (s *Store) CountMinutesByUser(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, COUNT(*) FROM minutes GROUP BY user_id`)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	counts := make(map[int64]int)

	for rows.Next() {
		var (
			userID int64
			count  int
		)

		if err = rows.Scan(&userID, &count); err != nil {
			return nil, err
		}

		counts[userID] = count
	}

	return counts, rows.Err()
}
