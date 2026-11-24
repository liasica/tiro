package store

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// 运行参数的默认值与下限
const (
	DefaultPollInterval       = 2 * time.Minute
	MinPollInterval           = 30 * time.Second
	DefaultFolderName         = "会议纪要"
	DefaultMinTranscriptChars = 200
)

// SummaryModelID 是未添加对话模型接口时沿用纪要模型的那一项
const SummaryModelID = "summary"

// 对话模型接口的格式，取值与 llm 包一致
const (
	APIChatCompletions = "chat_completions"
	APIResponses       = "responses"
)

// ChatProvider 是一个对话模型接口，Models 是勾选给对话使用的模型
type ChatProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	// API 是接口格式，为空时按地址推断
	API    string   `json:"api"`
	Models []string `json:"models"`
}

// APIFormat 返回接口格式：没有指定时，OpenAI 官方地址用 Responses，其余用 Chat Completions
func (p ChatProvider) APIFormat() string {
	if p.API == APIChatCompletions || p.API == APIResponses {
		return p.API
	}

	return DefaultAPIFormat(p.BaseURL)
}

// DefaultAPIFormat 按接口地址推断格式，OpenAI 官方的新模型只在 Responses 里支持推理与工具调用同时使用
func DefaultAPIFormat(baseURL string) string {
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Hostname() == "api.openai.com" {
		return APIResponses
	}

	return APIChatCompletions
}

// ChatModel 是对话可选的一个模型，ID 由接口 ID 与模型名组成
type ChatModel struct {
	ID      string
	Name    string
	BaseURL string
	APIKey  string
	Model   string
	API     string
}

// ChatModelID 拼出对话模型的 ID，例如「a1b2c3d4:gpt-5」
func ChatModelID(providerID, model string) string {
	return providerID + ":" + model
}

// Config 是从设置表读出的全部运行配置
type Config struct {
	AppID              string
	AppSecret          string
	LLMBaseURL         string
	LLMAPIKey          string
	LLMModel           string
	PollInterval       time.Duration
	FolderName         string
	MinTranscriptChars int
	Glossary           string
	// Prompt 为空表示使用内置提示词
	Prompt     string
	LastSyncAt time.Time
	// ChatProviders 是后台添加的对话模型接口，为空时对话沿用纪要的模型接口
	ChatProviders    []ChatProvider
	ChatDefaultModel string
}

// Config 读取运行配置，未设置的项取默认值
func (s *Store) Config(ctx context.Context) (*Config, error) {
	values, err := s.Settings(
		ctx,
		KeyFeishuAppID,
		KeyFeishuAppSecret,
		KeyLLMBaseURL,
		KeyLLMAPIKey,
		KeyLLMModel,
		KeyPollInterval,
		KeyFolderName,
		KeyMinTranscriptChars,
		KeyGlossary,
		KeyPrompt,
		KeyLastSyncAt,
		KeyChatProviders,
		KeyChatDefaultModel,
	)
	if err != nil {
		return nil, err
	}

	config := &Config{
		AppID:              values[KeyFeishuAppID],
		AppSecret:          values[KeyFeishuAppSecret],
		LLMBaseURL:         values[KeyLLMBaseURL],
		LLMAPIKey:          values[KeyLLMAPIKey],
		LLMModel:           values[KeyLLMModel],
		PollInterval:       DefaultPollInterval,
		FolderName:         DefaultFolderName,
		MinTranscriptChars: DefaultMinTranscriptChars,
		Glossary:           values[KeyGlossary],
		Prompt:             values[KeyPrompt],
		ChatDefaultModel:   values[KeyChatDefaultModel],
	}

	// 解析不了时当作没有添加对话模型接口
	if raw := values[KeyChatProviders]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &config.ChatProviders)
	}

	if seconds, ok := parseNumber(values[KeyPollInterval]); ok && seconds > 0 {
		config.PollInterval = max(time.Duration(seconds)*time.Second, MinPollInterval)
	}

	if name := values[KeyFolderName]; name != "" {
		config.FolderName = name
	}

	if chars, ok := parseNumber(values[KeyMinTranscriptChars]); ok && chars >= 0 {
		config.MinTranscriptChars = int(chars)
	}

	if seconds, ok := parseNumber(values[KeyLastSyncAt]); ok {
		config.LastSyncAt = fromUnix(seconds)
	}

	return config, nil
}

// parseNumber 解析整数，第二个返回值表示是否解析成功
func parseNumber(text string) (int64, bool) {
	number, err := strconv.ParseInt(text, 10, 64)

	return number, err == nil
}

// AppConfigured 判断飞书应用凭据是否已配置
func (c *Config) AppConfigured() bool {
	return c.AppID != "" && c.AppSecret != ""
}

// LLMConfigured 判断模型接口是否已配置，密钥允许为空
func (c *Config) LLMConfigured() bool {
	return c.LLMBaseURL != "" && c.LLMModel != ""
}

// ChatModelOptions 返回对话可选的模型，没有添加对话模型接口时沿用纪要的模型
//
// 有多个接口时，模型名前面加上接口名称以便区分
func (c *Config) ChatModelOptions() []ChatModel {
	if len(c.ChatProviders) == 0 {
		if !c.LLMConfigured() {
			return nil
		}

		return []ChatModel{{ID: SummaryModelID, Name: c.LLMModel, BaseURL: c.LLMBaseURL, APIKey: c.LLMAPIKey, Model: c.LLMModel}}
	}

	var options []ChatModel

	for _, provider := range c.ChatProviders {
		for _, model := range provider.Models {
			name := model

			if len(c.ChatProviders) > 1 {
				name = provider.DisplayName() + " · " + model
			}

			options = append(options, ChatModel{
				ID:      ChatModelID(provider.ID, model),
				Name:    name,
				BaseURL: provider.BaseURL,
				APIKey:  provider.APIKey,
				Model:   model,
				API:     provider.APIFormat(),
			})
		}
	}

	return options
}

// DisplayName 返回接口展示用的名称，没填名称时用接口地址的主机名
func (p ChatProvider) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}

	if parsed, err := url.Parse(p.BaseURL); err == nil && parsed.Host != "" {
		return parsed.Host
	}

	return p.BaseURL
}

// ChatModel 按 ID 选出对话模型，找不到时依次退回默认模型与第一个模型，没有可用模型时第二个返回值为 false
func (c *Config) ChatModel(id string) (ChatModel, bool) {
	options := c.ChatModelOptions()

	for _, wanted := range []string{id, c.ChatDefaultModel} {
		for _, option := range options {
			if wanted != "" && option.ID == wanted {
				return option, true
			}
		}
	}

	if len(options) == 0 {
		return ChatModel{}, false
	}

	return options[0], true
}
