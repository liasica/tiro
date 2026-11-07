package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/store"
)

const (
	// oauthCookie 暂存网页授权的 state 与授权后要回到的页面，内容为「state|页面路径」
	oauthCookie = "tiro_oauth"
	oauthPath   = "/api/auth/feishu"
	// oauthCallbackPath 需要填到飞书开发者后台的重定向 URL 里
	oauthCallbackPath = oauthPath + "/callback"
	oauthCookieMaxAge = 600
)

// handleOAuthBegin 跳转到飞书网页授权页，供飞书客户端内打开后台时免扫码登录
func (s *Server) handleOAuthBegin(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	if !config.AppConfigured() {
		redirectLoginError(w, r, "飞书应用尚未配置")

		return
	}

	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		writeInternalError(w, err)

		return
	}

	state := hex.EncodeToString(random)

	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Value:    state + "|" + url.QueryEscape(localPath(r.URL.Query().Get("next"))),
		Path:     oauthPath,
		MaxAge:   oauthCookieMaxAge,
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, s.feishu.AuthorizeURL(config.AppID, externalOrigin(r)+oauthCallbackPath, state), http.StatusFound)
}

// handleOAuthCallback 接收飞书网页授权的回调，换取令牌后保存用户并登录
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(oauthCookie)
	if err != nil {
		redirectLoginError(w, r, "登录已超时，请重新打开")

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Path:     oauthPath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
	})

	state, escapedNext, _ := strings.Cut(cookie.Value, "|")
	query := r.URL.Query()

	if subtle.ConstantTimeCompare([]byte(state), []byte(query.Get("state"))) != 1 {
		redirectLoginError(w, r, "登录校验失败，请重新打开")

		return
	}

	if query.Get("error") != "" {
		redirectLoginError(w, r, "已在飞书里拒绝授权")

		return
	}

	var config *store.Config

	config, err = s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	credential := feishu.AppCredential{AppID: config.AppID, AppSecret: config.AppSecret}

	var token *feishu.Token

	token, err = s.feishu.ExchangeCode(r.Context(), credential, query.Get("code"), externalOrigin(r)+oauthCallbackPath)
	if err != nil {
		slog.Warn("网页授权换取令牌失败", "error", err)
		redirectLoginError(w, r, "飞书授权失败: "+err.Error())

		return
	}

	var user *store.User

	user, err = s.saveAuthorization(r.Context(), token)
	if err != nil {
		slog.Warn("网页授权保存用户失败", "error", err)
		redirectLoginError(w, r, "飞书授权失败: "+err.Error())

		return
	}

	next, _ := url.QueryUnescape(escapedNext)

	s.setSession(w, r, user.ID)
	http.Redirect(w, r, localPath(next), http.StatusFound)
}

// redirectLoginError 回到登录页并带上失败原因，登录页据此停止自动跳转授权
func redirectLoginError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/?login_error="+url.QueryEscape(message), http.StatusFound)
}

// localPath 只接受本站内的路径，防止授权后被带到外部地址
func localPath(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, "/\\") {
		return "/"
	}

	return target
}

// externalOrigin 还原浏览器访问本服务所用的协议与主机，包括经反向代理转发的情况
func externalOrigin(r *http.Request) string {
	scheme := "http"

	if isSecure(r) {
		scheme = "https"
	}

	host, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")

	host = strings.TrimSpace(host)
	if host == "" {
		host = r.Host
	}

	return scheme + "://" + host
}
