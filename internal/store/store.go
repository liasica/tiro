// Package store 负责 SQLite 持久化，保存设置、授权用户与妙记处理记录
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// 纯 Go 实现的 SQLite 驱动，不依赖 cgo
	_ "modernc.org/sqlite"
)

// ErrNotFound 表示查询的记录不存在
var ErrNotFound = errors.New("记录不存在")

// 设置项的键
const (
	KeyFeishuAppID        = "feishu_app_id"
	KeyFeishuAppSecret    = "feishu_app_secret"
	KeySessionSecret      = "session_secret"
	KeyLLMBaseURL         = "llm_base_url"
	KeyLLMAPIKey          = "llm_api_key"
	KeyLLMModel           = "llm_model"
	KeyPollInterval       = "poll_interval_seconds"
	KeyFolderName         = "folder_name"
	KeyMinTranscriptChars = "min_transcript_chars"
	KeyGlossary           = "glossary"
	KeyPrompt             = "prompt"
	KeyLastSyncAt         = "last_sync_at"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	open_id            TEXT NOT NULL UNIQUE,
	name               TEXT NOT NULL DEFAULT '',
	avatar_url         TEXT NOT NULL DEFAULT '',
	role               TEXT NOT NULL DEFAULT 'member',
	enabled            INTEGER NOT NULL DEFAULT 1,
	access_token       TEXT NOT NULL DEFAULT '',
	refresh_token      TEXT NOT NULL DEFAULT '',
	access_expires_at  INTEGER NOT NULL DEFAULT 0,
	refresh_expires_at INTEGER NOT NULL DEFAULT 0,
	auth_status        TEXT NOT NULL DEFAULT 'active',
	folder_token       TEXT NOT NULL DEFAULT '',
	authorized_at      INTEGER NOT NULL DEFAULT 0,
	last_sync_at       INTEGER NOT NULL DEFAULT 0,
	created_at         INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS minutes (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id           INTEGER NOT NULL REFERENCES users(id),
	minute_token      TEXT NOT NULL,
	minute_url        TEXT NOT NULL DEFAULT '',
	title             TEXT NOT NULL DEFAULT '',
	minute_created_at INTEGER NOT NULL DEFAULT 0,
	duration_seconds  INTEGER NOT NULL DEFAULT 0,
	status            TEXT NOT NULL DEFAULT 'pending',
	reason            TEXT NOT NULL DEFAULT '',
	attempts          INTEGER NOT NULL DEFAULT 0,
	next_attempt_at   INTEGER NOT NULL DEFAULT 0,
	doc_url           TEXT NOT NULL DEFAULT '',
	summary_markdown  TEXT NOT NULL DEFAULT '',
	doc_title         TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL,
	finished_at       INTEGER NOT NULL DEFAULT 0,
	UNIQUE (user_id, minute_token)
);

CREATE INDEX IF NOT EXISTS idx_minutes_status ON minutes (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_minutes_user ON minutes (user_id, minute_created_at);
`

// Store 封装数据库连接
type Store struct {
	db *sql.DB
}

// Open 打开数据目录下的数据库并建表
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	dsn := "file:" + filepath.Join(dataDir, "tiro.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// SQLite 只有一个写者，单连接避免写锁竞争
	db.SetMaxOpenConns(1)

	if _, err = db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("初始化表结构失败: %w", err)
	}

	return &Store{db: db}, nil
}

// Close 关闭数据库
func (s *Store) Close() error {
	return s.db.Close()
}

// Setting 读取单个设置项，不存在时返回空串
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var value string

	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	return value, err
}

// Settings 批量读取设置项，不存在的键不出现在结果里
func (s *Store) Settings(ctx context.Context, keys ...string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return values, nil
	}

	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}

	query := `SELECT key, value FROM settings WHERE key IN (?` + strings.Repeat(",?", len(keys)-1) + `)`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return nil, err
		}

		values[key] = value
	}

	return values, rows.Err()
}

// SetSettings 在一个事务里写入多个设置项
func (s *Store) SetSettings(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if err = upsertSettings(ctx, tx, values); err != nil {
		return err
	}

	return tx.Commit()
}

// InitAppCredential 写入飞书应用凭据，仅当尚未配置时生效，已配置时返回 false
//
// 检查与写入在同一个事务里，两个人同时扫码建应用时只有先完成的那个生效
func (s *Store) InitAppCredential(ctx context.Context, appID, appSecret string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}

	defer func() { _ = tx.Rollback() }()

	var existing string

	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, KeyFeishuAppID).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	if existing != "" {
		return false, nil
	}

	err = upsertSettings(ctx, tx, map[string]string{KeyFeishuAppID: appID, KeyFeishuAppSecret: appSecret})
	if err != nil {
		return false, err
	}

	return true, tx.Commit()
}

// upsertSettings 在事务里逐项写入设置
func upsertSettings(ctx context.Context, tx *sql.Tx, values map[string]string) error {
	const upsert = `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`

	for key, value := range values {
		if _, err := tx.ExecContext(ctx, upsert, key, value); err != nil {
			return err
		}
	}

	return nil
}

// changed 把写操作的结果转成「是否有行被修改」
func changed(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}

	var affected int64

	affected, err = result.RowsAffected()

	return affected > 0, err
}

// toUnix 把时间转成秒级时间戳，零值存 0
func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}

	return t.Unix()
}

// fromUnix 把秒级时间戳还原成时间，0 还原为零值
func fromUnix(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}

	return time.Unix(seconds, 0)
}
