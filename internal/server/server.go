// Package server 提供后台界面用的 HTTP 接口与内嵌的前端资源
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/worker"
)

// 请求体大小上限，术语表与提示词都是文本，远小于这个值
const maxRequestBytes = 1 << 20

// Server 持有处理请求所需的全部依赖
type Server struct {
	store   *store.Store
	feishu  *feishu.Client
	worker  *worker.Worker
	flows   *flowManager
	assets  fs.FS
	version string
	// sessionSecret 用来给登录 cookie 签名
	sessionSecret []byte
}

// Options 是创建 Server 的参数，Assets 为 nil 表示前端尚未构建
type Options struct {
	Store   *store.Store
	Feishu  *feishu.Client
	Worker  *worker.Worker
	Assets  fs.FS
	Version string
}

// New 创建 Server，首次启动时生成会话签名密钥并存入数据库
func New(ctx context.Context, options Options) (*Server, error) {
	secret, err := options.Store.Setting(ctx, store.KeySessionSecret)
	if err != nil {
		return nil, err
	}

	if secret == "" {
		random := make([]byte, 32)
		if _, err = rand.Read(random); err != nil {
			return nil, fmt.Errorf("生成会话密钥失败: %w", err)
		}

		secret = hex.EncodeToString(random)

		if err = options.Store.SetSettings(ctx, map[string]string{store.KeySessionSecret: secret}); err != nil {
			return nil, err
		}
	}

	server := &Server{
		store:         options.Store,
		feishu:        options.Feishu,
		worker:        options.Worker,
		assets:        options.Assets,
		version:       options.Version,
		sessionSecret: []byte(secret),
	}

	server.flows = newFlowManager(server)

	return server, nil
}

// Handler 返回注册好全部路由的处理器
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/setup/app/begin", s.handleAppBegin)
	mux.HandleFunc("POST /api/setup/app/manual", s.handleAppManual)
	mux.HandleFunc("POST /api/auth/begin", s.handleAuthBegin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/flows/{id}", s.handleFlow)

	mux.HandleFunc("GET /api/minutes", s.withUser(s.handleMinuteList))
	mux.HandleFunc("POST /api/minutes", s.withUser(s.handleMinuteAdd))
	mux.HandleFunc("GET /api/minutes/{id}", s.withUser(s.handleMinuteDetail))
	mux.HandleFunc("POST /api/minutes/{id}/generate", s.withUser(s.handleMinuteGenerate))
	mux.HandleFunc("GET /api/stats", s.withUser(s.handleStats))

	mux.HandleFunc("GET /api/settings", s.withAdmin(s.handleSettingsGet))
	mux.HandleFunc("PUT /api/settings", s.withAdmin(s.handleSettingsPut))
	mux.HandleFunc("POST /api/settings/llm/test", s.withAdmin(s.handleLLMTest))
	mux.HandleFunc("GET /api/users", s.withAdmin(s.handleUserList))
	mux.HandleFunc("PATCH /api/users/{id}", s.withAdmin(s.handleUserPatch))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "接口不存在")
	})
	mux.HandleFunc("/", s.handleAssets)

	return mux
}

// userHandler 是需要登录用户的处理函数
type userHandler func(w http.ResponseWriter, r *http.Request, user *store.User)

// withUser 要求请求带有有效的登录 cookie
func (s *Server) withUser(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r)
		if user == nil {
			writeError(w, http.StatusUnauthorized, "尚未登录")

			return
		}

		next(w, r, user)
	}
}

// withAdmin 要求登录用户是管理员
func (s *Server) withAdmin(next userHandler) http.HandlerFunc {
	return s.withUser(func(w http.ResponseWriter, r *http.Request, user *store.User) {
		if user.Role != store.RoleAdmin {
			writeError(w, http.StatusForbidden, "只有管理员可以执行这个操作")

			return
		}

		next(w, r, user)
	})
}

// handleAssets 返回内嵌的前端文件，找不到的路径一律回到入口页交给前端路由
func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		http.Error(w, "前端尚未构建，执行 make web 后重新编译", http.StatusNotFound)

		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}

	if info, err := fs.Stat(s.assets, name); err != nil || info.IsDir() {
		name = "index.html"
	}

	// 带内容指纹的资源可以长期缓存，入口页每次都要重新取
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	http.ServeFileFS(w, r, s.assets, name)
}

// writeJSON 输出 JSON 响应
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Warn("写出响应失败", "error", err)
	}
}

// writeError 输出统一格式的错误响应
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeInternalError 记录内部错误并返回 500
func writeInternalError(w http.ResponseWriter, err error) {
	slog.Error("处理请求出错", "error", err)
	writeError(w, http.StatusInternalServerError, "服务器内部错误: "+err.Error())
}

// readJSON 把请求体解码到 target，请求体为空时保持 target 不变
func readJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))

	if err := decoder.Decode(target); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("请求体不是合法的 JSON")
	}

	return nil
}

// timeText 把时间写成 RFC 3339 字符串，零值返回 nil 以便输出为 null
func timeText(moment time.Time) *string {
	if moment.IsZero() {
		return nil
	}

	text := moment.Format(time.RFC3339)

	return &text
}
