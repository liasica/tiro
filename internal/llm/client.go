// Package llm 调用 OpenAI 格式的对话补全接口
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// 长逐字稿的生成耗时可达数分钟
	requestTimeout = 15 * time.Minute

	maxResponseBytes  = 16 << 20
	errorSnippetBytes = 300
)

// Config 是模型接口的连接参数
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Configured 判断接口地址与模型名是否都已填写，密钥允许为空
func (c Config) Configured() bool {
	return c.BaseURL != "" && c.Model != ""
}

var httpClient = &http.Client{Timeout: requestTimeout}

// Chat 发送一轮系统提示词加用户内容，返回模型的回复文本
func Chat(ctx context.Context, config Config, system, user string) (string, error) {
	if !config.Configured() {
		return "", errors.New("模型接口尚未配置")
	}

	messages := make([]map[string]string, 0, 2)

	if system != "" {
		messages = append(messages, map[string]string{"role": "system", "content": system})
	}

	messages = append(messages, map[string]string{"role": "user", "content": user})

	encoded, err := json.Marshal(map[string]any{
		"model":    config.Model,
		"messages": messages,
		"stream":   false,
	})
	if err != nil {
		return "", fmt.Errorf("编码请求失败: %w", err)
	}

	endpoint := strings.TrimRight(config.BaseURL, "/") + "/chat/completions"

	var httpRequest *http.Request

	httpRequest, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", fmt.Errorf("模型接口地址无效: %w", err)
	}

	httpRequest.Header.Set("Content-Type", "application/json")

	if config.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+config.APIKey)
	}

	var response *http.Response

	response, err = httpClient.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("请求模型接口失败: %w", err)
	}

	defer func() { _ = response.Body.Close() }()

	var raw []byte

	raw, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("读取模型接口响应失败: %w", err)
	}

	if response.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("模型接口返回 %d: %s", response.StatusCode, snippet(raw))
	}

	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err = json.Unmarshal(raw, &completion); err != nil {
		return "", fmt.Errorf("模型接口返回了非 JSON 内容: %s", snippet(raw))
	}

	if completion.Error != nil && completion.Error.Message != "" {
		return "", fmt.Errorf("模型接口返回错误: %s", completion.Error.Message)
	}

	if len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return "", errors.New("模型接口没有返回内容")
	}

	return completion.Choices[0].Message.Content, nil
}

// snippet 截取响应体开头一段用于报错
func snippet(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > errorSnippetBytes {
		text = text[:errorSnippetBytes] + "…"
	}

	return strings.ToValidUTF8(text, "")
}
