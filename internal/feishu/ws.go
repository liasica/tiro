package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	pathWSEndpoint = "/callback/ws/endpoint"

	// 服务端没有下发 ping 间隔时的默认值
	defaultPingInterval = 120 * time.Second
	// 读超时在两个 ping 间隔之外再留的余量
	pongGracePeriod = 5 * time.Second
	// 单次写入的超时
	wsWriteTimeout = 10 * time.Second
	// 单条消息的读取上限，库的默认值 32 KB 会把稍大的事件读断
	wsReadLimit = 16 << 20
	// 分包缓存的有效期，从收到最后一个分包算起
	fragmentTTL = 5 * time.Second

	headerType      = "type"
	headerMessageID = "message_id"
	headerSum       = "sum"
	headerSeq       = "seq"
	headerTraceID   = "trace_id"
	headerBizRT     = "biz_rt"

	frameTypeEvent = "event"
	frameTypePing  = "ping"
	frameTypePong  = "pong"

	// 飞书鉴权失败时 Handshake-Autherrcode 给出的错误码，表示应用的长连接数已达上限
	authCodeExceedConnLimit = "1000040350"
)

// ackPayload 是事件应答的 payload，code 取 HTTP 状态码
var ackPayload = []byte(`{"code":200,"headers":null,"data":null}`)

// EventHandler 处理一条事件，payload 是事件的完整 JSON（schema 2.0，含 header 与 event）
type EventHandler func(ctx context.Context, payload []byte)

// wsEndpoint 是长连接的接入地址与服务端下发的客户端配置
type wsEndpoint struct {
	URL          string          `json:"URL"`
	ClientConfig *wsClientConfig `json:"ClientConfig"`
}

// wsClientConfig 是服务端下发的客户端配置，重连相关的字段由调用方自行决定，这里只用 ping 间隔
type wsClientConfig struct {
	// PingInterval 单位为秒
	PingInterval int `json:"PingInterval"`
}

// ListenEvents 以应用身份建立长连接接收事件，阻塞到连接断开或 ctx 结束
//
// 连接建立后调用一次 onReady；每个 event 类型的数据帧先应答，再在独立的 goroutine 里交给 handle，handle 可能在本方法返回后仍在运行
//
// 只做单次连接，重连由调用方负责：ctx 结束时正常关闭连接并返回 ctx.Err()，其余情况返回断开原因
func (c *Client) ListenEvents(ctx context.Context, credential AppCredential, onReady func(), handle EventHandler) error {
	endpoint, err := c.fetchWSEndpoint(ctx, credential)
	if err != nil {
		return err
	}

	// 接入地址的查询参数里带着连接凭据，日志与错误信息只用主机名
	var endpointURL *url.URL

	endpointURL, err = url.Parse(endpoint.URL)
	if err != nil || endpointURL.Host == "" {
		return errors.New("飞书返回的长连接地址无效")
	}

	query := endpointURL.Query()

	var serviceID int64

	if raw := query.Get("service_id"); raw != "" {
		serviceID, err = strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return fmt.Errorf("飞书返回的长连接地址里 service_id 无效: %s", raw)
		}
	}

	// 握手，失败时把飞书在响应头里给出的原因转成错误
	var (
		conn     *websocket.Conn
		response *http.Response
	)

	conn, response, err = websocket.Dial(ctx, endpoint.URL, &websocket.DialOptions{HTTPClient: c.httpClient})
	if err != nil {
		if response != nil && response.StatusCode != http.StatusSwitchingProtocols {
			return handshakeError(response)
		}

		// 底层的 url.Error 会带上完整地址，去掉这一层
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return fmt.Errorf("连接飞书长连接 %s 失败: %w", endpointURL.Host, err)
	}

	conn.SetReadLimit(wsReadLimit)

	session := &wsSession{
		conn:      conn,
		serviceID: int32(serviceID),
		handle:    handle,
		fragments: map[string]*fragmentBuffer{},
	}

	pingInterval := defaultPingInterval

	if endpoint.ClientConfig != nil && endpoint.ClientConfig.PingInterval > 0 {
		pingInterval = time.Duration(endpoint.ClientConfig.PingInterval) * time.Second
	}

	session.pingInterval.Store(int64(pingInterval))

	slog.Info("飞书长连接已建立", "host", endpointURL.Host, "device_id", query.Get("device_id"))

	err = session.run(ctx, onReady)
	if ctx.Err() != nil {
		slog.Info("飞书长连接已关闭", "host", endpointURL.Host)

		return ctx.Err()
	}

	slog.Warn("飞书长连接已断开", "host", endpointURL.Host, "error", err)

	return err
}

// fetchWSEndpoint 用应用凭据换取长连接的接入地址
func (c *Client) fetchWSEndpoint(ctx context.Context, credential AppCredential) (*wsEndpoint, error) {
	encoded, err := json.Marshal(map[string]string{
		"AppID":     credential.AppID,
		"AppSecret": credential.AppSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("编码请求失败: %w", err)
	}

	var httpRequest *http.Request

	httpRequest, err = http.NewRequestWithContext(ctx, http.MethodPost, c.openBase+pathWSEndpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}

	httpRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	httpRequest.Header.Set("locale", "zh")

	var (
		raw    []byte
		status int
	)

	raw, status, err = c.do(httpRequest)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Code    int         `json:"code"`
		Message string      `json:"msg"`
		Data    *wsEndpoint `json:"data"`
	}

	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("飞书长连接接入地址接口返回了非 JSON 内容，HTTP %d", status)
	}

	if envelope.Code != 0 {
		return nil, fmt.Errorf("获取飞书长连接接入地址失败: %w", &APIError{HTTPStatus: status, Code: envelope.Code, Message: envelope.Message})
	}

	if status >= http.StatusBadRequest {
		return nil, fmt.Errorf("获取飞书长连接接入地址失败: %w", &APIError{HTTPStatus: status, Code: status, Message: http.StatusText(status)})
	}

	if envelope.Data == nil || envelope.Data.URL == "" {
		return nil, errors.New("飞书没有返回长连接接入地址")
	}

	return envelope.Data, nil
}

// handshakeError 把握手失败的响应转成可读的错误
func handshakeError(response *http.Response) error {
	status := response.Header.Get("Handshake-Status")
	if status == "" {
		status = strconv.Itoa(response.StatusCode)
	}

	text := "飞书长连接握手失败 " + status

	if message := response.Header.Get("Handshake-Msg"); message != "" {
		text += ": " + message
	}

	switch authCode := response.Header.Get("Handshake-Autherrcode"); authCode {
	case "":
	case authCodeExceedConnLimit:
		text += "，应用的长连接数已达上限"
	default:
		text += "，鉴权错误码 " + authCode
	}

	return errors.New(text)
}

// wsSession 是一条已建立的长连接
type wsSession struct {
	conn      *websocket.Conn
	serviceID int32
	handle    EventHandler

	// 写操作串行执行
	writeMutex sync.Mutex

	// pingInterval 是当前的 ping 间隔，单位纳秒，收到 pong 时可能被更新
	pingInterval atomic.Int64

	// fragments 按 message_id 缓存未收齐的分包，只在读循环里访问
	fragments map[string]*fragmentBuffer
}

// fragmentBuffer 是一条消息已收到的分包
type fragmentBuffer struct {
	parts     [][]byte
	updatedAt time.Time
}

// run 启动 ping 协程并阻塞在读循环上，直到连接断开或 ctx 结束
func (s *wsSession) run(ctx context.Context, onReady func()) error {
	// sessionCtx 在读循环退出后取消，用来收起 ping 协程与关闭协程
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var waitGroup sync.WaitGroup

	waitGroup.Go(func() {
		s.pingLoop(sessionCtx)
	})
	waitGroup.Go(func() {
		<-sessionCtx.Done()

		// ctx 结束时走关闭握手；读循环出错退出时连接已不可用，直接关闭
		if ctx.Err() != nil {
			_ = s.conn.Close(websocket.StatusNormalClosure, "")

			return
		}

		_ = s.conn.CloseNow()
	})

	if onReady != nil {
		onReady()
	}

	err := s.readLoop(ctx)

	cancel()
	waitGroup.Wait()

	return err
}

// pingLoop 建连后立刻发一次 ping，之后按当前的 ping 间隔定时发送
func (s *wsSession) pingLoop(ctx context.Context) {
	ping := &wsFrame{
		Service: s.serviceID,
		Method:  frameMethodControl,
		Headers: []wsHeader{{Key: headerType, Value: frameTypePing}},
	}

	for {
		if err := s.write(ping); err != nil {
			// 写失败说明连接已不可用，关闭连接让读循环退出
			if ctx.Err() == nil {
				slog.Warn("发送飞书长连接心跳失败", "error", err)

				_ = s.conn.CloseNow()
			}

			return
		}

		timer := time.NewTimer(time.Duration(s.pingInterval.Load()))

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}
	}
}

// readLoop 逐帧读取并处理，每次读取的超时是两个 ping 间隔再加余量
//
// 读取不受 ctx 取消的影响，ctx 结束时由关闭协程走关闭握手，读取随之返回
func (s *wsSession) readLoop(ctx context.Context) error {
	for {
		timeout := 2*time.Duration(s.pingInterval.Load()) + pongGracePeriod
		readCtx, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), timeout)

		messageType, message, err := s.conn.Read(readCtx)
		timedOut := errors.Is(readCtx.Err(), context.DeadlineExceeded)

		cancelRead()

		if err != nil {
			if timedOut {
				return fmt.Errorf("飞书长连接 %s 内没有收到数据", timeout)
			}

			return fmt.Errorf("读取飞书长连接失败: %w", err)
		}

		// 关闭过程中收到的事件不再应答，飞书会重新推送
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if messageType != websocket.MessageBinary {
			slog.Warn("飞书长连接收到非二进制消息", "type", messageType.String())

			continue
		}

		var frame *wsFrame

		frame, err = unmarshalFrame(message)
		if err != nil {
			slog.Warn("解码飞书长连接帧失败", "error", err)

			continue
		}

		switch frame.Method {
		case frameMethodControl:
			s.handleControl(frame)
		case frameMethodData:
			s.handleData(ctx, frame, time.Now())
		}
	}
}

// handleControl 处理控制帧，pong 里带着新的客户端配置时更新 ping 间隔
func (s *wsSession) handleControl(frame *wsFrame) {
	if frame.header(headerType) != frameTypePong || len(frame.Payload) == 0 {
		return
	}

	var config wsClientConfig
	if err := json.Unmarshal(frame.Payload, &config); err != nil {
		slog.Warn("解析飞书长连接客户端配置失败", "error", err)

		return
	}

	if config.PingInterval > 0 {
		s.pingInterval.Store(int64(time.Duration(config.PingInterval) * time.Second))
	}
}

// handleData 处理数据帧：分包先合并，收齐后立即应答，event 类型再交给 handle
func (s *wsSession) handleData(ctx context.Context, frame *wsFrame, receivedAt time.Time) {
	payload := frame.Payload

	if sum, _ := strconv.Atoi(frame.header(headerSum)); sum > 1 {
		seq, _ := strconv.Atoi(frame.header(headerSeq))

		payload = s.combine(frame.header(headerMessageID), sum, seq, payload, receivedAt)
		if payload == nil {
			return
		}
	}

	// 复用收到的帧作为应答，飞书 3 秒内收不到应答会重新推送，所以不等业务处理
	bizRT := strconv.FormatInt(time.Since(receivedAt).Milliseconds(), 10)

	frame.Headers = append(frame.Headers, wsHeader{Key: headerBizRT, Value: bizRT})
	frame.Payload = ackPayload

	if err := s.write(frame); err != nil {
		if ctx.Err() == nil {
			slog.Warn(
				"应答飞书长连接事件失败",
				"message_id",
				frame.header(headerMessageID),
				"trace_id",
				frame.header(headerTraceID),
				"error",
				err,
			)

			_ = s.conn.CloseNow()
		}

		return
	}

	if frame.header(headerType) != frameTypeEvent || s.handle == nil {
		return
	}

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("处理飞书事件时发生 panic", "panic", recovered)
			}
		}()

		s.handle(ctx, payload)
	}()
}

// combine 缓存一个分包，收齐后按 seq 顺序拼接返回，未收齐时返回 nil
func (s *wsSession) combine(messageID string, sum, seq int, part []byte, now time.Time) []byte {
	// 先丢掉过期的残包
	for id, buffer := range s.fragments {
		if now.Sub(buffer.updatedAt) > fragmentTTL {
			delete(s.fragments, id)
		}
	}

	if seq < 0 || seq >= sum {
		slog.Warn(
			"飞书长连接分包序号无效",
			"message_id",
			messageID,
			"sum",
			sum,
			"seq",
			seq,
		)

		return nil
	}

	buffer := s.fragments[messageID]
	if buffer == nil || len(buffer.parts) != sum {
		buffer = &fragmentBuffer{parts: make([][]byte, sum)}
		s.fragments[messageID] = buffer
	}

	buffer.parts[seq] = part
	buffer.updatedAt = now

	for _, item := range buffer.parts {
		if item == nil {
			return nil
		}
	}

	delete(s.fragments, messageID)

	return bytes.Join(buffer.parts, nil)
}

// write 编码并发送一帧，写操作互斥
//
// 写入不受 ctx 取消的影响，避免关闭握手前连接被提前断开
func (s *wsSession) write(frame *wsFrame) error {
	encoded := frame.marshal()

	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()

	writeCtx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()

	return s.conn.Write(writeCtx, websocket.MessageBinary, encoded)
}
