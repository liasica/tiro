package store

import (
	"context"
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
