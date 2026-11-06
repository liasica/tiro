package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liasica/tiro/internal/store"
)

const (
	sessionCookie   = "tiro_session"
	sessionLifetime = 30 * 24 * time.Hour
)

// sign 计算会话内容的签名
func (s *Server) sign(payload string) string {
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write([]byte(payload))

	return hex.EncodeToString(mac.Sum(nil))
}

// setSession 写入登录 cookie，内容为「用户 ID.过期时间.签名」
func (s *Server) setSession(w http.ResponseWriter, r *http.Request, userID int64) {
	expiresAt := time.Now().Add(sessionLifetime)
	payload := strconv.FormatInt(userID, 10) + "." + strconv.FormatInt(expiresAt.Unix(), 10)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    payload + "." + s.sign(payload),
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSession 清除登录 cookie
func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// currentUser 从 cookie 还原登录用户，未登录或 cookie 无效时返回 nil
func (s *Server) currentUser(r *http.Request) *store.User {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}

	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return nil
	}

	payload := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(s.sign(payload)), []byte(parts[2])) {
		return nil
	}

	var expiresAt int64

	expiresAt, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > expiresAt {
		return nil
	}

	var userID int64

	userID, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil
	}

	var user *store.User

	user, err = s.store.User(r.Context(), userID)
	if err != nil {
		return nil
	}

	return user
}

// isSecure 判断请求是否经由 HTTPS 到达，包括反向代理转发的情况
func isSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
