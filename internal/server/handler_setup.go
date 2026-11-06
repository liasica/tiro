package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/store"
)

// 初始化所处的阶段
const (
	stageApp   = "app"
	stageAuth  = "auth"
	stageLLM   = "llm"
	stageReady = "ready"
)

// userView 是返回给前端的当前用户信息
type userView struct {
	ID        int64  `json:"id"`
	OpenID    string `json:"open_id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Role      string `json:"role"`
	// AuthStatus 为 expired 时前端提示重新扫码
	AuthStatus string `json:"auth_status"`
}

// flowView 是发起扫码流程后返回给前端的信息
type flowView struct {
	FlowID    string `json:"flow_id"`
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
}

// handleState 返回初始化阶段与当前登录用户
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	stage := stageReady

	switch {
	case !config.AppConfigured():
		stage = stageApp
	case !config.LLMConfigured():
		stage = stageLLM
	}

	if stage != stageApp {
		var total int
		if total, err = s.store.CountUsers(r.Context()); err != nil {
			writeInternalError(w, err)

			return
		}

		if total == 0 {
			stage = stageAuth
		}
	}

	state := struct {
		Stage   string    `json:"stage"`
		Version string    `json:"version"`
		AppID   string    `json:"app_id"`
		User    *userView `json:"user"`
	}{Stage: stage, Version: s.version, AppID: config.AppID}

	if user := s.currentUser(r); user != nil {
		state.User = &userView{
			ID:         user.ID,
			OpenID:     user.OpenID,
			Name:       user.Name,
			AvatarURL:  user.AvatarURL,
			Role:       user.Role,
			AuthStatus: user.AuthStatus,
		}
	}

	writeJSON(w, http.StatusOK, state)
}

// handleAppBegin 发起扫码建应用，仅在应用未配置时可用
func (s *Server) handleAppBegin(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	if config.AppConfigured() {
		writeError(w, http.StatusConflict, "飞书应用已经配置过了")

		return
	}

	var device *feishu.DeviceFlow

	device, err = s.feishu.BeginAppRegistration(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "发起扫码失败: "+err.Error())

		return
	}

	s.respondFlow(w, device, s.pollAppRegistration(device.DeviceCode))
}

// handleAppManual 手动填写已有应用的凭据，仅在应用未配置时可用
func (s *Server) handleAppManual(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}

	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	if config.AppConfigured() {
		writeError(w, http.StatusConflict, "飞书应用已经配置过了")

		return
	}

	credential := feishu.AppCredential{
		AppID:     strings.TrimSpace(input.AppID),
		AppSecret: strings.TrimSpace(input.AppSecret),
	}

	if err = s.feishu.VerifyCredential(r.Context(), credential); err != nil {
		writeError(w, http.StatusBadRequest, "应用凭据校验失败: "+err.Error())

		return
	}

	err = s.store.SetSettings(r.Context(), map[string]string{
		store.KeyFeishuAppID:     credential.AppID,
		store.KeyFeishuAppSecret: credential.AppSecret,
	})
	if err != nil {
		writeInternalError(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleAuthBegin 发起扫码授权，授权成功即登录
func (s *Server) handleAuthBegin(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	if !config.AppConfigured() {
		writeError(w, http.StatusConflict, "飞书应用尚未配置")

		return
	}

	credential := feishu.AppCredential{AppID: config.AppID, AppSecret: config.AppSecret}

	var device *feishu.DeviceFlow

	device, err = s.feishu.BeginDeviceAuthorization(r.Context(), credential)
	if err != nil {
		writeError(w, http.StatusBadGateway, "发起扫码失败: "+err.Error())

		return
	}

	s.respondFlow(w, device, s.pollAuthorization(credential, device.DeviceCode))
}

// respondFlow 登记流程并把二维码链接返回给前端
func (s *Server) respondFlow(w http.ResponseWriter, device *feishu.DeviceFlow, poll pollFunc) {
	flowID, err := s.flows.start(device, poll)
	if errors.Is(err, errTooManyFlows) {
		writeError(w, http.StatusTooManyRequests, err.Error())

		return
	}

	if err != nil {
		writeInternalError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, flowView{
		FlowID:    flowID,
		URL:       device.VerificationURL,
		ExpiresIn: int(device.ExpiresIn.Seconds()),
	})
}

// handleFlow 返回扫码流程的状态，授权流程成功时顺带签发登录 cookie
func (s *Server) handleFlow(w http.ResponseWriter, r *http.Request) {
	current, ok := s.flows.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "二维码已失效")

		return
	}

	if current.status == flowSuccess && current.userID != 0 {
		s.setSession(w, r, current.userID)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": current.status, "error": current.message})
}

// handleLogout 退出登录
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}
