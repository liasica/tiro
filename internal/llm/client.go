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

// Usage 是一次调用的用量
type Usage struct {
	// Model 取接口返回的实际模型名，接口没有返回时取配置的模型名
	Model string
	// PromptTokens 是输入 token 数，包含缓存命中的部分
	PromptTokens int64
	CachedTokens int64
	// CompletionTokens 是输出 token 数，包含推理的部分
	CompletionTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
	Duration         time.Duration
}

// completionUsage 是响应里的 usage 字段，兼容在顶层给出缓存命中数的接口
type completionUsage struct {
	PromptTokens         int64 `json:"prompt_tokens"`
	CompletionTokens     int64 `json:"completion_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

var httpClient = &http.Client{Timeout: requestTimeout}

// Chat 发送一轮系统提示词加用户内容，返回模型的回复文本与本次调用的用量
//
// 出错时用量照常返回，接口已计费但内容不可用的调用也能统计到
func Chat(ctx context.Context, config Config, system, user string) (string, Usage, error) {
	usage := Usage{Model: config.Model}

	if !config.Configured() {
		return "", usage, errors.New("模型接口尚未配置")
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
		return "", usage, fmt.Errorf("编码请求失败: %w", err)
	}

	endpoint := strings.TrimRight(config.BaseURL, "/") + "/chat/completions"

	var httpRequest *http.Request

	httpRequest, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", usage, fmt.Errorf("模型接口地址无效: %w", err)
	}

	httpRequest.Header.Set("Content-Type", "application/json")

	if config.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+config.APIKey)
	}

	var response *http.Response

	startedAt := time.Now()

	response, err = httpClient.Do(httpRequest)
	if err != nil {
		return "", usage, fmt.Errorf("请求模型接口失败: %w", err)
	}

	defer func() { _ = response.Body.Close() }()

	var raw []byte

	raw, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))

	usage.Duration = time.Since(startedAt)

	if err != nil {
		return "", usage, fmt.Errorf("读取模型接口响应失败: %w", err)
	}

	if response.StatusCode >= http.StatusBadRequest {
		return "", usage, fmt.Errorf("模型接口返回 %d: %s", response.StatusCode, snippet(raw))
	}

	var completion struct {
		Model   string           `json:"model"`
		Usage   *completionUsage `json:"usage"`
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
		return "", usage, fmt.Errorf("模型接口返回了非 JSON 内容: %s", snippet(raw))
	}

	if completion.Model != "" {
		usage.Model = completion.Model
	}

	usage.fill(completion.Usage)

	if completion.Error != nil && completion.Error.Message != "" {
		return "", usage, fmt.Errorf("模型接口返回错误: %s", completion.Error.Message)
	}

	if len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return "", usage, errors.New("模型接口没有返回内容")
	}

	return completion.Choices[0].Message.Content, usage, nil
}

// fill 用响应里的 usage 字段填写 token 数，接口没给总数时按输入加输出计算
func (u *Usage) fill(reported *completionUsage) {
	if reported == nil {
		return
	}

	u.PromptTokens = reported.PromptTokens
	u.CompletionTokens = reported.CompletionTokens
	u.TotalTokens = reported.TotalTokens
	u.CachedTokens = max(reported.PromptTokensDetails.CachedTokens, reported.PromptCacheHitTokens)
	u.ReasoningTokens = reported.CompletionTokensDetails.ReasoningTokens

	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
}

// snippet 截取响应体开头一段用于报错
func snippet(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > errorSnippetBytes {
		text = text[:errorSnippetBytes] + "…"
	}

	return strings.ToValidUTF8(text, "")
}
