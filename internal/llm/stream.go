package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// 消息角色
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// 流式响应单行的长度上限，单个增量远小于这个值
const maxStreamLineBytes = 4 << 20

// Message 是对话里的一条消息，字段与 OpenAI 格式一致
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ReasoningContent 是部分推理模型要求在工具调用轮次里原样回传的思考内容
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

// MarshalJSON 在只有工具调用、没有正文时把 content 写成 null，部分接口不接受空串
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message

	if m.Content != "" || len(m.ToolCalls) == 0 {
		return json.Marshal(plain(m))
	}

	return json.Marshal(struct {
		plain

		Content *string `json:"content"`
	}{plain: plain(m)})
}

// ToolCall 是模型发起的一次工具调用
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall 是工具调用的函数名与 JSON 编码的参数
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool 是提供给模型的工具定义
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction 是工具的名称、说明与参数的 JSON Schema
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// streamChunk 是流式响应里的一个增量
type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int          `json:"index"`
				ID       string       `json:"id"`
				Type     string       `json:"type"`
				Function FunctionCall `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
		// 部分接口把用量放在 choice 里
		Usage *completionUsage `json:"usage"`
	} `json:"choices"`
	Usage *completionUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ToolChoiceNone 禁止模型调用工具，工具定义照常提供
const ToolChoiceNone = "none"

// Request 是一轮对话请求
type Request struct {
	Messages []Message
	Tools    []Tool
	// ToolChoice 为空时由模型决定是否调用工具
	ToolChoice string
}

// Stream 以流式方式发送一轮对话，正文增量实时交给 onDelta，返回完整的助手消息与本次调用的用量
//
// 出错时照常返回已经收到的部分消息与用量；接口不支持流式、直接返回完整 JSON 时也能处理
func Stream(ctx context.Context, config Config, request Request, onDelta func(string)) (*Message, Usage, error) {
	startedAt := time.Now()

	reply, usage, err := stream(ctx, config, request, onDelta)

	usage.Duration = time.Since(startedAt)

	return reply, usage, err
}

// stream 发出请求并读完响应，耗时由 Stream 统计
func stream(ctx context.Context, config Config, request Request, onDelta func(string)) (*Message, Usage, error) {
	usage := Usage{Model: config.Model}
	reply := &Message{Role: RoleAssistant}

	if !config.Configured() {
		return reply, usage, errors.New("模型接口尚未配置")
	}

	payload := map[string]any{
		"model":          config.Model,
		"messages":       request.Messages,
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
	}

	if len(request.Tools) > 0 {
		payload["tools"] = request.Tools
	}

	if request.ToolChoice != "" {
		payload["tool_choice"] = request.ToolChoice
	}

	response, err := post(ctx, config, "/chat/completions", payload)
	if err != nil {
		return reply, usage, err
	}

	defer func() { _ = response.Body.Close() }()

	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "application/json" {
		err = readCompletion(response.Body, reply, &usage, onDelta)
	} else {
		err = readStream(response.Body, reply, &usage, onDelta)
	}

	if err != nil {
		return reply, usage, err
	}

	if strings.TrimSpace(reply.Content) == "" && len(reply.ToolCalls) == 0 {
		return reply, usage, errors.New("模型接口没有返回内容")
	}

	return reply, usage, nil
}

// post 以流式请求调用模型接口，返回状态正常的响应，调用方负责关闭响应体
func post(ctx context.Context, config Config, path string, payload any) (*http.Response, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码请求失败: %w", err)
	}

	endpoint := strings.TrimRight(config.BaseURL, "/") + path

	var httpRequest *http.Request

	httpRequest, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("模型接口地址无效: %w", err)
	}

	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")

	if config.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+config.APIKey)
	}

	var response *http.Response

	response, err = httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("请求模型接口失败: %w", err)
	}

	if response.StatusCode >= http.StatusBadRequest {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, errorSnippetBytes*4))

		_ = response.Body.Close()

		return nil, fmt.Errorf("模型接口返回 %d: %s", response.StatusCode, snippet(raw))
	}

	return response, nil
}

// sseData 逐行读取 SSE 流，把每个 data 行交给 handle，handle 返回 true 时提前结束
func sseData(body io.Reader, handle func(data string) (bool, error)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxStreamLineBytes)

	for scanner.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "data:")
		if !ok {
			continue
		}

		done, err := handle(strings.TrimSpace(data))
		if err != nil || done {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取模型接口响应失败: %w", err)
	}

	return nil
}

// readStream 逐行读取 SSE 流，把增量拼进 reply
func readStream(body io.Reader, reply *Message, usage *Usage, onDelta func(string)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxStreamLineBytes)

	calls := newToolCallBuilder()

	var (
		content   strings.Builder
		reasoning strings.Builder
		// finished 表示收到了结束标记，没有收到就断开说明响应不完整
		finished bool
	)

	defer func() {
		reply.Content = content.String()
		reply.ReasoningContent = reasoning.String()
		reply.ToolCalls = calls.result()
	}()

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}

		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return nil
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("模型接口返回了无法解析的增量: %s", snippet([]byte(data)))
		}

		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("模型接口返回错误: %s", chunk.Error.Message)
		}

		if chunk.Model != "" {
			usage.Model = chunk.Model
		}

		usage.fill(chunk.Usage)

		for _, choice := range chunk.Choices {
			usage.fill(choice.Usage)

			if choice.FinishReason != "" {
				finished = true
			}

			if text := choice.Delta.Content; text != "" {
				content.WriteString(text)

				if onDelta != nil {
					onDelta(text)
				}
			}

			reasoning.WriteString(choice.Delta.ReasoningContent)

			for _, delta := range choice.Delta.ToolCalls {
				calls.add(delta.Index, delta.ID, delta.Type, delta.Function)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取模型接口响应失败: %w", err)
	}

	// 有的接口不发 [DONE]，只要给过结束原因也算完整
	if !finished {
		return errors.New("模型接口的响应中途断开")
	}

	return nil
}

// readCompletion 处理不支持流式、直接返回完整结果的接口
func readCompletion(body io.Reader, reply *Message, usage *Usage, onDelta func(string)) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("读取模型接口响应失败: %w", err)
	}

	var completion struct {
		Model   string           `json:"model"`
		Usage   *completionUsage `json:"usage"`
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err = json.Unmarshal(raw, &completion); err != nil {
		return fmt.Errorf("模型接口返回了非 JSON 内容: %s", snippet(raw))
	}

	if completion.Model != "" {
		usage.Model = completion.Model
	}

	usage.fill(completion.Usage)

	if completion.Error != nil && completion.Error.Message != "" {
		return fmt.Errorf("模型接口返回错误: %s", completion.Error.Message)
	}

	if len(completion.Choices) == 0 {
		return nil
	}

	message := completion.Choices[0].Message

	reply.Content = message.Content
	reply.ReasoningContent = message.ReasoningContent
	reply.ToolCalls = message.ToolCalls

	if reply.Content != "" && onDelta != nil {
		onDelta(reply.Content)
	}

	return nil
}

// toolCallBuilder 把按序号分片到达的工具调用拼成完整调用
type toolCallBuilder struct {
	calls []*ToolCall
	// slots 记录增量里的序号对应 calls 的哪一项
	slots map[int]int
}

func newToolCallBuilder() *toolCallBuilder {
	return &toolCallBuilder{slots: make(map[int]int)}
}

// add 合并一个增量
//
// 有的接口每个调用都用序号 0 一次给全，序号相同但 ID 不同时当成新的调用
func (b *toolCallBuilder) add(index int, id, callType string, function FunctionCall) {
	slot, ok := b.slots[index]
	if ok && id != "" && b.calls[slot].ID != "" && b.calls[slot].ID != id {
		ok = false
	}

	if !ok {
		b.calls = append(b.calls, &ToolCall{Type: "function"})
		slot = len(b.calls) - 1
		b.slots[index] = slot
	}

	call := b.calls[slot]

	if id != "" {
		call.ID = id
	}

	if callType != "" {
		call.Type = callType
	}

	call.Function.Name += function.Name
	call.Function.Arguments += function.Arguments
}

func (b *toolCallBuilder) result() []ToolCall {
	calls := make([]ToolCall, 0, len(b.calls))

	for _, call := range b.calls {
		if call.Function.Name == "" {
			continue
		}

		calls = append(calls, *call)
	}

	return calls
}

// Add 把另一次调用的用量累加进来，模型名取最后一次的
func (u *Usage) Add(other Usage) {
	if other.Model != "" {
		u.Model = other.Model
	}

	u.PromptTokens += other.PromptTokens
	u.CachedTokens += other.CachedTokens
	u.CompletionTokens += other.CompletionTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.TotalTokens += other.TotalTokens
	u.Duration += other.Duration
}
