package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Card 是机器人私聊发出的消息卡片内容
type Card struct {
	Title string
	// Template 是标题栏配色，取飞书卡片的模板色名，如 blue、red
	Template   string
	Markdown   string
	ButtonText string
	ButtonURL  string
}

// SendCard 以应用身份给用户发一张消息卡片
func (c *Client) SendCard(ctx context.Context, credential AppCredential, openID string, card Card) error {
	tenantToken, err := c.TenantToken(ctx, credential)
	if err != nil {
		return err
	}

	elements := []map[string]any{
		{"tag": "markdown", "content": card.Markdown},
	}

	if card.ButtonURL != "" {
		elements = append(elements, map[string]any{
			"tag":  "button",
			"type": "primary",
			"text": map[string]string{"tag": "plain_text", "content": card.ButtonText},
			"behaviors": []map[string]string{
				{"type": "open_url", "default_url": card.ButtonURL},
			},
		})
	}

	var content []byte

	content, err = json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"title":    map[string]string{"tag": "plain_text", "content": card.Title},
			"template": card.Template,
		},
		"body": map[string]any{"elements": elements},
	})
	if err != nil {
		return fmt.Errorf("编码消息卡片失败: %w", err)
	}

	query := url.Values{}
	query.Set("receive_id_type", "open_id")

	return c.openAPI(ctx, apiRequest{
		method:      http.MethodPost,
		path:        "/open-apis/im/v1/messages",
		accessToken: tenantToken,
		query:       query,
		payload: map[string]string{
			"receive_id": openID,
			"msg_type":   "interactive",
			"content":    string(content),
		},
	}, nil)
}

// tenantTokenRequest 用应用凭据换取应用身份的访问令牌
//
// 这个接口的令牌字段在响应顶层而不是 data 里，不能走 openAPI
func (c *Client) tenantTokenRequest(ctx context.Context, credential AppCredential, result any) error {
	encoded, err := json.Marshal(map[string]string{
		"app_id":     credential.AppID,
		"app_secret": credential.AppSecret,
	})
	if err != nil {
		return fmt.Errorf("编码请求失败: %w", err)
	}

	var httpRequest *http.Request

	endpoint := c.openBase + "/open-apis/auth/v3/tenant_access_token/internal"

	httpRequest, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}

	httpRequest.Header.Set("Content-Type", "application/json; charset=utf-8")

	var (
		raw    []byte
		status int
	)

	raw, status, err = c.do(httpRequest)
	if err != nil {
		return err
	}

	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"msg"`
	}

	if err = json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("飞书应用凭据接口返回了非 JSON 内容，HTTP %d", status)
	}

	if envelope.Code != 0 {
		return &APIError{HTTPStatus: status, Code: envelope.Code, Message: envelope.Message}
	}

	if err = json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("解析飞书应用凭据接口的响应失败: %w", err)
	}

	return nil
}

// VerifyCredential 校验应用凭据是否有效
func (c *Client) VerifyCredential(ctx context.Context, credential AppCredential) error {
	if credential.AppID == "" || credential.AppSecret == "" {
		return errors.New("应用的 App ID 与 App Secret 都不能为空")
	}

	_, err := c.TenantToken(ctx, credential)

	return err
}
