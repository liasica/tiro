package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 用户角色与授权状态
const (
	RoleAdmin  = "admin"
	RoleMember = "member"

	AuthActive  = "active"
	AuthExpired = "expired"
)

// User 是一个完成飞书授权的使用者
type User struct {
	ID               int64
	OpenID           string
	Name             string
	AvatarURL        string
	Role             string
	Enabled          bool
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	AuthStatus       string
	FolderToken      string
	AuthorizedAt     time.Time
	LastSyncAt       time.Time
	CreatedAt        time.Time
}

const userColumns = `id, open_id, name, avatar_url, role, enabled, access_token, refresh_token,
	access_expires_at, refresh_expires_at, auth_status, folder_token, authorized_at, last_sync_at, created_at`

// scanner 同时适配 *sql.Row 与 *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (*User, error) {
	var (
		user                                                         User
		enabled                                                      int
		accessExpires, refreshExpires, authorized, lastSync, created int64
	)

	err := row.Scan(
		&user.ID,
		&user.OpenID,
		&user.Name,
		&user.AvatarURL,
		&user.Role,
		&enabled,
		&user.AccessToken,
		&user.RefreshToken,
		&accessExpires,
		&refreshExpires,
		&user.AuthStatus,
		&user.FolderToken,
		&authorized,
		&lastSync,
		&created,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	user.Enabled = enabled == 1
	user.AccessExpiresAt = fromUnix(accessExpires)
	user.RefreshExpiresAt = fromUnix(refreshExpires)
	user.AuthorizedAt = fromUnix(authorized)
	user.LastSyncAt = fromUnix(lastSync)
	user.CreatedAt = fromUnix(created)

	return &user, nil
}

// SaveAuthorizedUser 保存一次授权的结果
//
// 用户已存在时刷新资料与令牌并恢复授权状态；不存在时新建，库里第一个用户成为管理员
func (s *Store) SaveAuthorizedUser(ctx context.Context, authorized *User) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = tx.Rollback() }()

	now := time.Now()

	var existingID int64

	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE open_id = ?`, authorized.OpenID).Scan(&existingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var total int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
			return nil, err
		}

		role := RoleMember

		if total == 0 {
			role = RoleAdmin
		}

		var result sql.Result

		result, err = tx.ExecContext(
			ctx,
			`INSERT INTO users (open_id, name, avatar_url, role, access_token, refresh_token,
				access_expires_at, refresh_expires_at, auth_status, authorized_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			authorized.OpenID,
			authorized.Name,
			authorized.AvatarURL,
			role,
			authorized.AccessToken,
			authorized.RefreshToken,
			toUnix(authorized.AccessExpiresAt),
			toUnix(authorized.RefreshExpiresAt),
			AuthActive,
			now.Unix(),
			now.Unix(),
		)
		if err != nil {
			return nil, err
		}

		if existingID, err = result.LastInsertId(); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		_, err = tx.ExecContext(
			ctx,
			`UPDATE users SET name = ?, avatar_url = ?, access_token = ?, refresh_token = ?,
				access_expires_at = ?, refresh_expires_at = ?, auth_status = ? WHERE id = ?`,
			authorized.Name,
			authorized.AvatarURL,
			authorized.AccessToken,
			authorized.RefreshToken,
			toUnix(authorized.AccessExpiresAt),
			toUnix(authorized.RefreshExpiresAt),
			AuthActive,
			existingID,
		)
		if err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return s.User(ctx, existingID)
}

// User 按主键读取用户
func (s *Store) User(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// Users 读取全部用户，按创建顺序排列
func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var users []*User

	for rows.Next() {
		var user *User
		if user, err = scanUser(rows); err != nil {
			return nil, err
		}

		users = append(users, user)
	}

	return users, rows.Err()
}

// CountUsers 返回用户总数
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var total int

	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total)

	return total, err
}

// UpdateUserTokens 写入续期后的令牌
func (s *Store) UpdateUserTokens(ctx context.Context, user *User) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE users SET access_token = ?, refresh_token = ?, access_expires_at = ?, refresh_expires_at = ?,
			auth_status = ? WHERE id = ?`,
		user.AccessToken,
		user.RefreshToken,
		toUnix(user.AccessExpiresAt),
		toUnix(user.RefreshExpiresAt),
		AuthActive,
		user.ID,
	)

	return err
}

// ExpireUserAuth 把授权标成失效，仅当库里的刷新令牌仍是 refreshToken 时生效
//
// 续期失败的同时用户重新扫码登录的话，新令牌已经写入，此时不再标成失效并返回 false
func (s *Store) ExpireUserAuth(ctx context.Context, id int64, refreshToken string) (bool, error) {
	const query = `UPDATE users SET auth_status = ? WHERE id = ? AND refresh_token = ? AND auth_status = ?`

	return changed(s.db.ExecContext(
		ctx,
		query,
		AuthExpired,
		id,
		refreshToken,
		AuthActive,
	))
}

// SetUserFolder 记录用户云空间里存放纪要的文件夹
func (s *Store) SetUserFolder(ctx context.Context, id int64, folderToken string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET folder_token = ? WHERE id = ?`, folderToken, id)

	return err
}

// SetUserLastSync 记录最近一次检查新妙记的时间
func (s *Store) SetUserLastSync(ctx context.Context, id int64, syncedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_sync_at = ? WHERE id = ?`, toUnix(syncedAt), id)

	return err
}

// SetUserEnabled 切换是否为该用户自动生成纪要
func (s *Store) SetUserEnabled(ctx context.Context, id int64, enabled bool) error {
	value := 0

	if enabled {
		value = 1
	}

	_, err := s.db.ExecContext(ctx, `UPDATE users SET enabled = ? WHERE id = ?`, value, id)

	return err
}

// SetUserRole 修改用户角色
func (s *Store) SetUserRole(ctx context.Context, id int64, role string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)

	return err
}

// CountAdmins 返回管理员人数
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var total int

	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = ?`, RoleAdmin).Scan(&total)

	return total, err
}

// ClearUserFolders 清空所有用户记录的纪要文件夹，下次生成时按当前文件夹名重新创建
func (s *Store) ClearUserFolders(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET folder_token = ''`)

	return err
}
