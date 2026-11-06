package server

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liasica/tiro/internal/llm"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/summary"
)

const (
	maxFolderNameChars    = 50
	maxMinTranscriptChars = 100000
	maxPollIntervalSecond = 86400

	// 测试连接的超时与回显长度
	llmTestTimeout    = 60 * time.Second
	llmTestReplyChars = 200
)

// settingsView 是返回给前端的设置，密钥只给出是否已设置
type settingsView struct {
	Feishu struct {
		AppID        string `json:"app_id"`
		AppSecretSet bool   `json:"app_secret_set"`
	} `json:"feishu"`
	LLM struct {
		BaseURL   string `json:"base_url"`
		Model     string `json:"model"`
		APIKeySet bool   `json:"api_key_set"`
	} `json:"llm"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	FolderName          string `json:"folder_name"`
	MinTranscriptChars  int    `json:"min_transcript_chars"`
	Glossary            string `json:"glossary"`
	Prompt              string `json:"prompt"`
	PromptIsDefault     bool   `json:"prompt_is_default"`
}

// llmInput 是模型接口的可写字段，nil 表示不修改
type llmInput struct {
	BaseURL *string `json:"base_url"`
	Model   *string `json:"model"`
	APIKey  *string `json:"api_key"`
}

// settingsInput 是修改设置的请求体，只处理出现的字段
type settingsInput struct {
	LLM                 *llmInput `json:"llm"`
	PollIntervalSeconds *int      `json:"poll_interval_seconds"`
	FolderName          *string   `json:"folder_name"`
	MinTranscriptChars  *int      `json:"min_transcript_chars"`
	Glossary            *string   `json:"glossary"`
	Prompt              *string   `json:"prompt"`
}

func newSettingsView(config *store.Config) settingsView {
	var view settingsView

	view.Feishu.AppID = config.AppID
	view.Feishu.AppSecretSet = config.AppSecret != ""
	view.LLM.BaseURL = config.LLMBaseURL
	view.LLM.Model = config.LLMModel
	view.LLM.APIKeySet = config.LLMAPIKey != ""
	view.PollIntervalSeconds = int(config.PollInterval.Seconds())
	view.FolderName = config.FolderName
	view.MinTranscriptChars = config.MinTranscriptChars
	view.Glossary = config.Glossary
	view.Prompt = config.Prompt
	view.PromptIsDefault = config.Prompt == ""

	if view.PromptIsDefault {
		view.Prompt = summary.DefaultPrompt()
	}

	return view
}

// respondSettings 读取最新配置并返回
func (s *Server) respondSettings(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, newSettingsView(config))
}

// handleSettingsGet 返回当前设置
func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request, _ *store.User) {
	s.respondSettings(w, r)
}

// handleSettingsPut 修改设置
func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request, _ *store.User) {
	var input settingsInput
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	values, folderChanged, message := s.collectSettings(r.Context(), input)
	if message != "" {
		writeError(w, http.StatusBadRequest, message)

		return
	}

	if len(values) > 0 {
		if err := s.store.SetSettings(r.Context(), values); err != nil {
			writeInternalError(w, err)

			return
		}
	}

	// 文件夹改名后，后续纪要放进按新名字创建的文件夹
	if folderChanged {
		if err := s.store.ClearUserFolders(r.Context()); err != nil {
			writeInternalError(w, err)

			return
		}
	}

	s.worker.Wake()
	s.respondSettings(w, r)
}

// collectSettings 校验请求并整理成待写入的设置项，校验不通过时 message 非空
func (s *Server) collectSettings(ctx context.Context, input settingsInput) (values map[string]string, folderChanged bool, message string) {
	values = make(map[string]string)

	config, err := s.store.Config(ctx)
	if err != nil {
		message = "读取当前设置失败: " + err.Error()

		return
	}

	if input.LLM != nil {
		if message = collectLLM(input.LLM, config, values); message != "" {
			return
		}
	}

	if input.PollIntervalSeconds != nil {
		seconds := *input.PollIntervalSeconds
		if seconds < int(store.MinPollInterval.Seconds()) || seconds > maxPollIntervalSecond {
			message = "检查间隔需在 " + strconv.Itoa(int(store.MinPollInterval.Seconds())) + " 秒到 " +
				strconv.Itoa(maxPollIntervalSecond) + " 秒之间"

			return
		}

		values[store.KeyPollInterval] = strconv.Itoa(seconds)
	}

	if input.FolderName != nil {
		name := strings.TrimSpace(*input.FolderName)
		if name == "" || utf8.RuneCountInString(name) > maxFolderNameChars {
			message = "文件夹名不能为空，且不超过 " + strconv.Itoa(maxFolderNameChars) + " 个字"

			return
		}

		folderChanged = name != config.FolderName
		values[store.KeyFolderName] = name
	}

	if input.MinTranscriptChars != nil {
		chars := *input.MinTranscriptChars
		if chars < 0 || chars > maxMinTranscriptChars {
			message = "逐字稿最短字数需在 0 到 " + strconv.Itoa(maxMinTranscriptChars) + " 之间"

			return
		}

		values[store.KeyMinTranscriptChars] = strconv.Itoa(chars)
	}

	if input.Glossary != nil {
		values[store.KeyGlossary] = strings.TrimSpace(*input.Glossary)
	}

	if input.Prompt != nil {
		prompt := strings.TrimSpace(*input.Prompt)

		// 与内置提示词相同时按默认处理，内置提示词更新后能自动跟上
		if prompt == summary.DefaultPrompt() {
			prompt = ""
		}

		values[store.KeyPrompt] = prompt
	}

	return
}

// collectLLM 校验模型接口的字段并写入 values，校验不通过时返回提示
func collectLLM(input *llmInput, config *store.Config, values map[string]string) string {
	if input.BaseURL != nil {
		baseURL := strings.TrimRight(strings.TrimSpace(*input.BaseURL), "/")
		if !validBaseURL(baseURL) {
			return "接口地址需以 http:// 或 https:// 开头"
		}

		values[store.KeyLLMBaseURL] = baseURL

		// 地址换了就丢弃原来的密钥，避免把它发到新地址；下面填了新密钥会再覆盖
		if baseURL != config.LLMBaseURL {
			values[store.KeyLLMAPIKey] = ""
		}
	}

	if input.Model != nil {
		model := strings.TrimSpace(*input.Model)
		if model == "" {
			return "模型名不能为空"
		}

		values[store.KeyLLMModel] = model
	}

	// 密钥留空表示不修改
	if input.APIKey != nil && strings.TrimSpace(*input.APIKey) != "" {
		values[store.KeyLLMAPIKey] = strings.TrimSpace(*input.APIKey)
	}

	return ""
}

func validBaseURL(baseURL string) bool {
	parsed, err := url.Parse(baseURL)

	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// handleLLMTest 用给定的参数向模型接口发一条短消息，返回是否连通
func (s *Server) handleLLMTest(w http.ResponseWriter, r *http.Request, _ *store.User) {
	var input struct {
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		APIKey  string `json:"api_key"`
	}

	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	config, err := s.store.Config(r.Context())
	if err != nil {
		writeInternalError(w, err)

		return
	}

	target := llm.Config{
		BaseURL: strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"),
		Model:   strings.TrimSpace(input.Model),
		APIKey:  strings.TrimSpace(input.APIKey),
	}

	if !validBaseURL(target.BaseURL) || target.Model == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "接口地址与模型名都需要填写"})

		return
	}

	// 没填密钥时沿用已保存的，但只在地址没变的情况下，避免把密钥发到别的地址
	if target.APIKey == "" && target.BaseURL == config.LLMBaseURL {
		target.APIKey = config.LLMAPIKey
	}

	ctx, cancel := context.WithTimeout(r.Context(), llmTestTimeout)
	defer cancel()

	startedAt := time.Now()

	var reply string

	reply, err = llm.Chat(ctx, target, "", "这是一次连接测试，只回复「连接正常」四个字")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})

		return
	}

	reply = strings.TrimSpace(reply)
	if utf8.RuneCountInString(reply) > llmTestReplyChars {
		reply = string([]rune(reply)[:llmTestReplyChars]) + "…"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"reply":      reply,
		"latency_ms": time.Since(startedAt).Milliseconds(),
	})
}
