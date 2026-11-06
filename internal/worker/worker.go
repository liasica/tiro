// Package worker 定时发现新妙记并把它们处理成会议纪要
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/store"
)

const (
	// 主循环的节拍，待处理记录最迟在这个间隔后被取走
	tickInterval = 5 * time.Second

	// 搜索起点比上次检查再早这么久，靠 token 去重
	//
	// 录音类妙记的创建时间是开始录音的时间，录完才会出现在搜索结果里，重叠要盖住最长的会议
	searchOverlap = 24 * time.Hour

	// 长时间停机或授权失效后恢复时，最多回看这么久
	searchMaxLookback = 30 * 24 * time.Hour

	// 访问令牌剩余有效期低于这个值时提前续期
	refreshAhead = 5 * time.Minute

	// 一条记录自动重试的次数上限
	maxAttempts = 3
)

// 第 n 次失败后到下一次重试的等待时间
var retryBackoff = [...]time.Duration{time.Minute, 5 * time.Minute}

// ErrReauthorize 表示用户的飞书授权已失效，需要重新扫码
var ErrReauthorize = errors.New("飞书授权已失效，需要重新扫码登录")

// ErrBusy 表示记录正在生成中，不能重新排队
var ErrBusy = errors.New("这条记录正在生成中")

// Worker 负责定时搜索与纪要生成
type Worker struct {
	store  *store.Store
	feishu *feishu.Client
	wake   chan struct{}

	// syncRequested 为真时下一轮不等检查间隔，立即搜索新妙记
	syncRequested atomic.Bool

	// 刷新令牌只能用一次，续期必须串行
	tokenMutex sync.Mutex
}

// New 创建 Worker
func New(database *store.Store, client *feishu.Client) *Worker {
	return &Worker{
		store:  database,
		feishu: client,
		wake:   make(chan struct{}, 1),
	}
}

// Wake 让主循环立即跑一轮，用于手动添加妙记或重新生成之后
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// RequestSync 让主循环立即搜索一次新妙记，用于有人刚完成授权或被重新启用之后
func (w *Worker) RequestSync() {
	w.syncRequested.Store(true)
	w.Wake()
}

// Run 运行主循环直到 ctx 结束
func (w *Worker) Run(ctx context.Context) {
	if err := w.store.ResetProcessing(ctx); err != nil {
		slog.Error("复位处理中的记录失败", "error", err)
	}

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	var lastSync time.Time

	for {
		config, err := w.store.Config(ctx)

		switch {
		case err != nil:
			slog.Error("读取配置失败", "error", err)
		case config.AppConfigured():
			if w.syncRequested.Swap(false) || time.Since(lastSync) >= config.PollInterval {
				w.syncAll(ctx, config)

				lastSync = time.Now()
			}

			w.processPending(ctx, config)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// syncAll 为每个启用且授权有效的用户搜索新妙记
func (w *Worker) syncAll(ctx context.Context, config *store.Config) {
	users, err := w.store.Users(ctx)
	if err != nil {
		slog.Error("读取用户列表失败", "error", err)

		return
	}

	for _, user := range users {
		if ctx.Err() != nil {
			return
		}

		if !user.Enabled || user.AuthStatus != store.AuthActive {
			continue
		}

		if err = w.syncUser(ctx, config, user); err != nil {
			slog.Warn("检查新妙记失败", "user", user.Name, "error", err)
		}
	}

	err = w.store.SetSettings(ctx, map[string]string{
		store.KeyLastSyncAt: strconv.FormatInt(time.Now().Unix(), 10),
	})
	if err != nil {
		slog.Error("记录检查时间失败", "error", err)
	}
}

// syncUser 搜索一个用户最近的妙记，把没见过的入库
//
// 首次授权之前创建的妙记只登记不生成，需要时可以在后台手动生成
func (w *Worker) syncUser(ctx context.Context, config *store.Config, user *store.User) error {
	accessToken, err := w.AccessToken(ctx, config, user)
	if err != nil {
		return err
	}

	var minuteTokens []string

	minuteTokens, err = w.feishu.SearchMinuteTokens(ctx, accessToken, user.OpenID, searchSince(user, time.Now()))
	if err != nil {
		return w.handleAuthError(ctx, config, user, err)
	}

	var known map[string]struct{}

	known, err = w.store.KnownMinuteTokens(ctx, user.ID)
	if err != nil {
		return err
	}

	for _, minuteToken := range minuteTokens {
		if _, seen := known[minuteToken]; seen {
			continue
		}

		var info *feishu.MinuteInfo

		info, err = w.feishu.MinuteInfo(ctx, accessToken, minuteToken)
		if feishu.IsAuthorizationInvalid(err) {
			return w.handleAuthError(ctx, config, user, err)
		}

		// 单条读取失败不影响其余妙记，下一轮检查还会再遇到它
		if err != nil {
			slog.Warn("读取妙记信息失败", "token", minuteToken, "error", err)

			continue
		}

		record := newRecord(user.ID, info)
		if !info.CreatedAt.IsZero() && info.CreatedAt.Before(user.AuthorizedAt) {
			record.Status = store.StatusSkipped
			record.Reason = "授权前生成的妙记"
		}

		if _, err = w.store.InsertMinute(ctx, record); err != nil {
			return err
		}

		slog.Info("发现新妙记", "user", user.Name, "title", info.Title)
	}

	return w.store.SetUserLastSync(ctx, user.ID, time.Now())
}

// searchSince 计算本次搜索的起始时间
func searchSince(user *store.User, now time.Time) time.Time {
	if user.LastSyncAt.IsZero() {
		return now.Add(-searchOverlap)
	}

	since := user.LastSyncAt.Add(-searchOverlap)
	if earliest := now.Add(-searchMaxLookback); since.Before(earliest) {
		return earliest
	}

	return since
}

// newRecord 用妙记的基本信息构造一条待处理记录
func newRecord(userID int64, info *feishu.MinuteInfo) *store.Minute {
	return &store.Minute{
		UserID:          userID,
		MinuteToken:     info.Token,
		MinuteURL:       info.URL,
		Title:           info.Title,
		MinuteCreatedAt: info.CreatedAt,
		DurationSeconds: int64(info.Duration.Seconds()),
		Status:          store.StatusPending,
	}
}

// AddMinute 手动登记一条妙记并排队生成，已登记过的重新排队
func (w *Worker) AddMinute(ctx context.Context, user *store.User, minuteToken string) (*store.Minute, error) {
	config, err := w.store.Config(ctx)
	if err != nil {
		return nil, err
	}

	var accessToken string

	accessToken, err = w.AccessToken(ctx, config, user)
	if err != nil {
		return nil, err
	}

	var info *feishu.MinuteInfo

	info, err = w.feishu.MinuteInfo(ctx, accessToken, minuteToken)
	if err != nil {
		return nil, w.handleAuthError(ctx, config, user, err)
	}

	var inserted bool

	inserted, err = w.store.InsertMinute(ctx, newRecord(user.ID, info))
	if err != nil {
		return nil, err
	}

	var record *store.Minute

	record, err = w.store.MinuteByToken(ctx, user.ID, minuteToken)
	if err != nil {
		return nil, err
	}

	if !inserted {
		var requeued bool

		requeued, err = w.store.RequeueMinute(ctx, record.ID)
		if err != nil {
			return nil, err
		}

		if !requeued {
			return nil, ErrBusy
		}

		if record, err = w.store.Minute(ctx, record.ID); err != nil {
			return nil, err
		}
	}

	w.Wake()

	return record, nil
}

// AccessToken 返回用户当前可用的访问令牌，快过期时先续期
func (w *Worker) AccessToken(ctx context.Context, config *store.Config, user *store.User) (string, error) {
	w.tokenMutex.Lock()
	defer w.tokenMutex.Unlock()

	// 重新读库，拿到别的调用刚续期写入的令牌
	current, err := w.store.User(ctx, user.ID)
	if err != nil {
		return "", err
	}

	if current.AuthStatus != store.AuthActive {
		return "", ErrReauthorize
	}

	if time.Until(current.AccessExpiresAt) > refreshAhead {
		return current.AccessToken, nil
	}

	if current.RefreshToken == "" || time.Now().After(current.RefreshExpiresAt) {
		w.expireAuthorization(ctx, config, current)

		return "", ErrReauthorize
	}

	var token *feishu.Token

	token, err = w.feishu.RefreshToken(ctx, credentialOf(config), current.RefreshToken)
	if err != nil {
		if feishu.IsAuthorizationInvalid(err) {
			w.expireAuthorization(ctx, config, current)

			return "", ErrReauthorize
		}

		return "", fmt.Errorf("续期飞书令牌失败: %w", err)
	}

	current.AccessToken = token.AccessToken
	current.AccessExpiresAt = token.AccessExpiresAt

	// 部分情况下续期不返回新的刷新令牌，此时沿用旧的
	if token.RefreshToken != "" {
		current.RefreshToken = token.RefreshToken
		current.RefreshExpiresAt = token.RefreshExpiresAt
	}

	if err = w.store.UpdateUserTokens(ctx, current); err != nil {
		return "", err
	}

	return current.AccessToken, nil
}

// handleAuthError 在接口报令牌无效时把用户标成需要重新授权，其余错误原样返回
func (w *Worker) handleAuthError(ctx context.Context, config *store.Config, user *store.User, cause error) error {
	if !feishu.IsAuthorizationInvalid(cause) {
		return cause
	}

	// 传进来的用户可能是较早读出的，令牌以库里当前的为准
	current, err := w.store.User(ctx, user.ID)
	if err != nil {
		return err
	}

	w.expireAuthorization(ctx, config, current)

	return ErrReauthorize
}

// expireAuthorization 标记授权失效并私聊通知本人
//
// 用户在此期间已经重新扫码登录时，库里的刷新令牌已换新，不再标成失效
func (w *Worker) expireAuthorization(ctx context.Context, config *store.Config, user *store.User) {
	expired, err := w.store.ExpireUserAuth(ctx, user.ID, user.RefreshToken)
	if err != nil {
		slog.Error("标记授权失效失败", "user", user.Name, "error", err)

		return
	}

	if !expired {
		return
	}

	slog.Warn("用户的飞书授权已失效", "user", user.Name)

	w.notify(ctx, config, user, feishu.Card{
		Title:    "飞书授权已失效",
		Template: "orange",
		Markdown: "重新在 tiro 后台扫码登录后，恢复自动生成会议纪要",
	})
}

// notify 给用户发消息卡片，失败只记日志
func (w *Worker) notify(ctx context.Context, config *store.Config, user *store.User, card feishu.Card) {
	if err := w.feishu.SendCard(ctx, credentialOf(config), user.OpenID, card); err != nil {
		slog.Warn("发送飞书通知失败", "user", user.Name, "error", err)
	}
}

func credentialOf(config *store.Config) feishu.AppCredential {
	return feishu.AppCredential{AppID: config.AppID, AppSecret: config.AppSecret}
}
