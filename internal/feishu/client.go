// Package feishu 封装本项目用到的飞书开放平台接口
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultOpenBase     = "https://open.feishu.cn"
	defaultAccountsBase = "https://accounts.feishu.cn"

	// 单次响应体的读取上限，逐字稿较长时也远小于这个值
	maxResponseBytes = 32 << 20
)

// Client 是飞书接口客户端，可并发使用
type Client struct {
	httpClient   *http.Client
	openBase     string
	accountsBase string

	tenantMutex     sync.Mutex
	tenantAppID     string
	tenantToken     string
	tenantExpiresAt time.Time
}

// New 创建客户端
//
// 环境变量 TIRO_FEISHU_OPEN_BASE 与 TIRO_FEISHU_ACCOUNTS_BASE 可以把请求指向别的地址，供本地联调使用
func New() *Client {
	return &Client{
		httpClient:   &http.Client{Timeout: 60 * time.Second},
		openBase:     envOr("TIRO_FEISHU_OPEN_BASE", defaultOpenBase),
		accountsBase: envOr("TIRO_FEISHU_ACCOUNTS_BASE", defaultAccountsBase),
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimRight(os.Getenv(name), "/"); value != "" {
		return value
	}

	return fallback
}

// APIError 是开放平台返回的业务错误
type APIError struct {
	HTTPStatus int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("飞书接口返回错误 %d: %s", e.Code, e.Message)
}

// OAuthError 是授权相关接口返回的错误，Code 取 OAuth 2.0 的 error 字段
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description == "" {
		return "飞书授权接口返回错误: " + e.Code
	}

	return fmt.Sprintf("飞书授权接口返回错误 %s: %s", e.Code, e.Description)
}

// openAPI 调用 open-apis 下的接口并把 data 字段解码到 result
//
// accessToken 为空时不带 Authorization 头；query、payload、result 都可以为 nil
func (c *Client) openAPI(ctx context.Context, request apiRequest, result any) error {
	target := c.openBase + request.path
	if len(request.query) > 0 {
		target += "?" + request.query.Encode()
	}

	var body io.Reader

	if request.payload != nil {
		encoded, err := json.Marshal(request.payload)
		if err != nil {
			return fmt.Errorf("编码请求失败: %w", err)
		}

		body = bytes.NewReader(encoded)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, request.method, target, body)
	if err != nil {
		return err
	}

	if request.payload != nil {
		httpRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	if request.accessToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+request.accessToken)
	}

	var (
		raw    []byte
		status int
	)

	raw, status, err = c.do(httpRequest)
	if err != nil {
		return err
	}

	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}

	if err = json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("飞书接口 %s 返回了非 JSON 内容，HTTP %d", request.path, status)
	}

	if envelope.Code != 0 {
		return &APIError{HTTPStatus: status, Code: envelope.Code, Message: envelope.Message}
	}

	if status >= http.StatusBadRequest {
		return &APIError{HTTPStatus: status, Code: status, Message: http.StatusText(status)}
	}

	if result == nil || len(envelope.Data) == 0 {
		return nil
	}

	if err = json.Unmarshal(envelope.Data, result); err != nil {
		return fmt.Errorf("解析飞书接口 %s 的响应失败: %w", request.path, err)
	}

	return nil
}

// apiRequest 描述一次 open-apis 调用
type apiRequest struct {
	method      string
	path        string
	accessToken string
	query       url.Values
	payload     any
}

// do 发出请求并读回完整响应体
func (c *Client) do(httpRequest *http.Request) ([]byte, int, error) {
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, 0, fmt.Errorf("请求飞书接口失败: %w", err)
	}

	defer func() { _ = response.Body.Close() }()

	var raw []byte

	raw, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, response.StatusCode, fmt.Errorf("读取飞书接口响应失败: %w", err)
	}

	return raw, response.StatusCode, nil
}

// oauthForm 以表单方式调用 accounts 域下的授权接口，返回解码后的 JSON 对象
func (c *Client) oauthForm(ctx context.Context, path string, form url.Values, basicAuth string) (map[string]any, error) {
	body := strings.NewReader(form.Encode())

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.accountsBase+path, body)
	if err != nil {
		return nil, err
	}

	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if basicAuth != "" {
		httpRequest.Header.Set("Authorization", "Basic "+basicAuth)
	}

	var (
		raw    []byte
		status int
	)

	raw, status, err = c.do(httpRequest)
	if err != nil {
		return nil, err
	}

	return decodeOAuth(raw, status, path)
}

// oauthJSON 以 JSON 方式调用 accounts 域下的授权接口
func (c *Client) oauthJSON(ctx context.Context, path string, payload any) (map[string]any, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码请求失败: %w", err)
	}

	var httpRequest *http.Request

	httpRequest, err = http.NewRequestWithContext(ctx, http.MethodPost, c.accountsBase+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}

	httpRequest.Header.Set("Content-Type", "application/json; charset=utf-8")

	var (
		raw    []byte
		status int
	)

	raw, status, err = c.do(httpRequest)
	if err != nil {
		return nil, err
	}

	return decodeOAuth(raw, status, path)
}

// decodeOAuth 解析授权接口的响应，带 error 字段时转成 OAuthError
func decodeOAuth(raw []byte, status int, path string) (map[string]any, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("飞书授权接口 %s 返回了非 JSON 内容，HTTP %d", path, status)
	}

	if code := stringField(data, "error"); code != "" {
		return nil, &OAuthError{Code: code, Description: stringField(data, "error_description")}
	}

	if status >= http.StatusBadRequest {
		return nil, &OAuthError{Code: fmt.Sprintf("http_%d", status), Description: stringField(data, "msg")}
	}

	return data, nil
}

func stringField(data map[string]any, key string) string {
	value, _ := data[key].(string)

	return value
}

func intField(data map[string]any, key string, fallback int) int {
	if value, ok := data[key].(float64); ok && value > 0 {
		return int(value)
	}

	return fallback
}
