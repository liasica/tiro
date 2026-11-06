package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/liasica/tiro/internal/store"
)

// memberView 是成员页里的一个用户
type memberView struct {
	ID           int64   `json:"id"`
	OpenID       string  `json:"open_id"`
	Name         string  `json:"name"`
	AvatarURL    string  `json:"avatar_url"`
	Role         string  `json:"role"`
	Enabled      bool    `json:"enabled"`
	AuthStatus   string  `json:"auth_status"`
	AuthorizedAt *string `json:"authorized_at"`
	LastSyncAt   *string `json:"last_sync_at"`
	MinutesTotal int     `json:"minutes_total"`
}

func newMemberView(user *store.User, minutesTotal int) memberView {
	return memberView{
		ID:           user.ID,
		OpenID:       user.OpenID,
		Name:         user.Name,
		AvatarURL:    user.AvatarURL,
		Role:         user.Role,
		Enabled:      user.Enabled,
		AuthStatus:   user.AuthStatus,
		AuthorizedAt: timeText(user.AuthorizedAt),
		LastSyncAt:   timeText(user.LastSyncAt),
		MinutesTotal: minutesTotal,
	}
}

// handleUserList 返回全部成员
func (s *Server) handleUserList(w http.ResponseWriter, r *http.Request, _ *store.User) {
	users, err := s.store.Users(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	var counts map[int64]int

	counts, err = s.store.CountMinutesByUser(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	items := make([]memberView, 0, len(users))
	for _, user := range users {
		items = append(items, newMemberView(user, counts[user.ID]))
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleUserPatch 修改成员的启用状态或角色
func (s *Server) handleUserPatch(w http.ResponseWriter, r *http.Request, _ *store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "成员 ID 无效")

		return
	}

	var input struct {
		Enabled *bool   `json:"enabled"`
		Role    *string `json:"role"`
	}

	if err = readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	var target *store.User

	target, err = s.store.User(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "成员不存在")

		return
	}

	if err != nil {
		writeInternalError(w, err)

		return
	}

	if input.Role != nil && *input.Role != target.Role {
		if message := s.changeRole(r, target, *input.Role); message != "" {
			writeError(w, http.StatusBadRequest, message)

			return
		}
	}

	if input.Enabled != nil {
		if err = s.store.SetUserEnabled(r.Context(), id, *input.Enabled); err != nil {
			writeInternalError(w, err)

			return
		}
	}

	if target, err = s.store.User(r.Context(), id); err != nil {
		writeInternalError(w, err)

		return
	}

	var counts map[int64]int

	counts, err = s.store.CountMinutesByUser(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	s.worker.RequestSync()
	writeJSON(w, http.StatusOK, newMemberView(target, counts[id]))
}

// changeRole 修改角色并保证至少留有一名管理员，不能修改时返回提示
func (s *Server) changeRole(r *http.Request, target *store.User, role string) string {
	if role != store.RoleAdmin && role != store.RoleMember {
		return "角色只能是 admin 或 member"
	}

	if target.Role == store.RoleAdmin {
		admins, err := s.store.CountAdmins(r.Context())
		if err != nil {
			return "读取管理员人数失败: " + err.Error()
		}

		if admins <= 1 {
			return "至少保留一名管理员"
		}
	}

	if err := s.store.SetUserRole(r.Context(), target.ID, role); err != nil {
		return "修改角色失败: " + err.Error()
	}

	return ""
}
