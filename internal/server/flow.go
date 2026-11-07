package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/store"
)

// 扫码流程的状态
const (
	flowPending = "pending"
	flowSuccess = "success"
	flowDenied  = "denied"
	flowExpired = "expired"
	flowError   = "error"
)

const (
	// 流程结束后保留多久供前端读取结果
	flowRetention = 10 * time.Minute

	// 同时进行的扫码流程数上限，发起扫码的接口不需要登录，靠它限制对飞书的请求量
	maxPendingFlows = 20
)

// errTooManyFlows 表示进行中的扫码流程已达上限
var errTooManyFlows = errors.New("正在进行的扫码过多，稍后再试")

// flow 是一次进行中的扫码流程
type flow struct {
	status  string
	message string
	// userID 只在授权流程成功后有值，用来签发登录 cookie
	userID   int64
	expireAt time.Time
}

// flowManager 在内存里跟踪扫码流程，并在后台轮询飞书直到流程结束
type flowManager struct {
	server *Server
	mutex  sync.Mutex
	flows  map[string]*flow
}

func newFlowManager(server *Server) *flowManager {
	return &flowManager{server: server, flows: make(map[string]*flow)}
}

// pollFunc 查询一次流程结果，尚未结束时返回 feishu 包里的中间状态错误
type pollFunc func(ctx context.Context) (userID int64, err error)

// start 登记一个流程并启动后台轮询，返回流程 ID
func (m *flowManager) start(device *feishu.DeviceFlow, poll pollFunc) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}

	flowID := "f_" + hex.EncodeToString(random)
	deadline := time.Now().Add(device.ExpiresIn)

	m.mutex.Lock()
	m.purge()

	if m.pending() >= maxPendingFlows {
		m.mutex.Unlock()

		return "", errTooManyFlows
	}

	m.flows[flowID] = &flow{status: flowPending, expireAt: deadline.Add(flowRetention)}
	m.mutex.Unlock()

	go m.run(flowID, device.Interval, deadline, poll)

	return flowID, nil
}

// run 按间隔轮询直到流程成功、被拒绝、出错或过期
func (m *flowManager) run(flowID string, interval time.Duration, deadline time.Time, poll pollFunc) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			m.finish(flowID, flowExpired, "二维码已过期", 0)

			return
		case <-time.After(interval):
		}

		userID, err := poll(ctx)

		switch {
		case err == nil:
			m.finish(flowID, flowSuccess, "", userID)

			return
		case errors.Is(err, feishu.ErrAuthorizationPending):
		case errors.Is(err, feishu.ErrSlowDown):
			interval += 5 * time.Second
		case errors.Is(err, feishu.ErrAccessDenied):
			m.finish(flowID, flowDenied, "已在飞书里拒绝", 0)

			return
		case errors.Is(err, feishu.ErrFlowExpired), ctx.Err() != nil:
			m.finish(flowID, flowExpired, "二维码已过期", 0)

			return
		default:
			slog.Warn("扫码流程出错", "error", err)
			m.finish(flowID, flowError, err.Error(), 0)

			return
		}
	}
}

func (m *flowManager) finish(flowID, status, message string, userID int64) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if current, ok := m.flows[flowID]; ok {
		current.status = status
		current.message = message
		current.userID = userID
	}
}

// get 返回流程的当前状态，流程不存在时第二个返回值为 false
func (m *flowManager) get(flowID string) (flow, bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	found, ok := m.flows[flowID]
	if !ok {
		return flow{}, false
	}

	return *found, true
}

// pending 返回进行中的流程数，调用方需持有锁
func (m *flowManager) pending() int {
	total := 0

	for _, current := range m.flows {
		if current.status == flowPending {
			total++
		}
	}

	return total
}

// purge 清掉早已结束的流程，调用方需持有锁
func (m *flowManager) purge() {
	now := time.Now()

	for flowID, current := range m.flows {
		if now.After(current.expireAt) {
			delete(m.flows, flowID)
		}
	}
}

// pollAppRegistration 查询扫码建应用的结果，拿到凭据后写入设置
func (s *Server) pollAppRegistration(deviceCode string) pollFunc {
	return func(ctx context.Context) (int64, error) {
		credential, err := s.feishu.PollAppRegistration(ctx, deviceCode)
		if err != nil {
			return 0, err
		}

		var saved bool

		saved, err = s.store.InitAppCredential(ctx, credential.AppID, credential.AppSecret)
		if err != nil {
			return 0, err
		}

		// 两个人同时扫码时只认先完成的那个
		if !saved {
			return 0, errors.New("飞书应用已经配置过了")
		}

		slog.Info("飞书应用已创建", "app_id", credential.AppID)

		return 0, nil
	}
}

// pollAuthorization 查询扫码授权的结果，成功后保存用户与令牌
func (s *Server) pollAuthorization(credential feishu.AppCredential, deviceCode string) pollFunc {
	return func(ctx context.Context) (int64, error) {
		token, err := s.feishu.PollDeviceToken(ctx, credential, deviceCode)
		if err != nil {
			return 0, err
		}

		var user *store.User

		user, err = s.saveAuthorization(ctx, token)
		if err != nil {
			return 0, err
		}

		return user.ID, nil
	}
}

// saveAuthorization 按令牌取回用户资料，保存用户与令牌并立即检查一次新妙记
func (s *Server) saveAuthorization(ctx context.Context, token *feishu.Token) (*store.User, error) {
	info, err := s.feishu.UserInfo(ctx, token.AccessToken)
	if err != nil {
		return nil, err
	}

	var user *store.User

	user, err = s.store.SaveAuthorizedUser(ctx, &store.User{
		OpenID:           info.OpenID,
		Name:             info.Name,
		AvatarURL:        info.AvatarURL,
		AccessToken:      token.AccessToken,
		RefreshToken:     token.RefreshToken,
		AccessExpiresAt:  token.AccessExpiresAt,
		RefreshExpiresAt: token.RefreshExpiresAt,
	})
	if err != nil {
		return nil, err
	}

	slog.Info("用户已授权", "name", user.Name, "role", user.Role)
	s.worker.RequestSync()

	return user, nil
}
