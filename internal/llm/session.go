package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Turn 是一轮请求的工具设置
type Turn struct {
	Tools []Tool
	// ToolChoice 为空时由模型决定是否调用工具
	ToolChoice string
}

// Session 是一段多轮对话，按接口格式维护上下文，Agent 的工具调用循环用它驱动
//
// Chat Completions 每轮都发送完整的消息列表；Responses 首轮发送全部消息，之后用上一轮的响应 ID 接续，只发新增的内容，模型的推理过程由接口保存
type Session struct {
	config Config
	// instructions 是系统提示词，Responses 每轮都要重新给出
	instructions string
	messages     []Message
	// previous 是 Responses 上一轮的响应 ID
	previous string
}

// NewSession 用系统提示词与历史消息开始一段对话，这些内容随第一轮请求发出
func NewSession(config Config, system string, history []Message) *Session {
	messages := make([]Message, 0, len(history)+1)

	if system != "" {
		messages = append(messages, Message{Role: RoleSystem, Content: system})
	}

	return &Session{config: config, instructions: system, messages: append(messages, history...)}
}

// Send 追加新的消息（用户提问或工具结果）并请求一轮回复，成功的回复记入上下文
func (s *Session) Send(ctx context.Context, input []Message, turn Turn, onDelta func(string)) (*Message, Usage, error) {
	if s.config.API != APIResponses {
		s.messages = append(s.messages, input...)

		reply, usage, err := Stream(ctx, s.config, Request{Messages: s.messages, Tools: turn.Tools, ToolChoice: turn.ToolChoice}, onDelta)
		if err == nil {
			s.messages = append(s.messages, *reply)
		}

		return reply, usage, err
	}

	// 首轮把历史一起发出，之后只发新增的内容
	items := input

	if s.previous == "" {
		items = append(s.historyWithoutSystem(), input...)
	}

	startedAt := time.Now()

	reply, usage, responseID, err := streamResponses(ctx, s.config, responsesRequest{
		instructions: s.instructions,
		previous:     s.previous,
		input:        items,
		turn:         turn,
	}, onDelta)

	usage.Duration = time.Since(startedAt)

	if err == nil {
		s.previous = responseID
	}

	return reply, usage, err
}

// historyWithoutSystem 返回系统提示词之外的历史消息，Responses 的系统提示词另用 instructions 给出
func (s *Session) historyWithoutSystem() []Message {
	history := make([]Message, 0, len(s.messages))

	for _, message := range s.messages {
		if message.Role != RoleSystem {
			history = append(history, message)
		}
	}

	return history
}

// responsesRequest 是一轮 Responses 请求的内容
type responsesRequest struct {
	instructions string
	previous     string
	input        []Message
	turn         Turn
}

// responsesEvent 是 Responses 流里用到的事件字段
type responsesEvent struct {
	Type    string `json:"type"`
	Delta   string `json:"delta"`
	Message string `json:"message"`
	Item    struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens        int64 `json:"input_tokens"`
			OutputTokens       int64 `json:"output_tokens"`
			TotalTokens        int64 `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

// streamResponses 以流式方式调用 Responses 接口，返回回复、用量与这一轮的响应 ID
func streamResponses(ctx context.Context, config Config, request responsesRequest, onDelta func(string)) (*Message, Usage, string, error) {
	usage := Usage{Model: config.Model}
	reply := &Message{Role: RoleAssistant}

	if !config.Configured() {
		return reply, usage, "", errors.New("模型接口尚未配置")
	}

	payload := map[string]any{
		"model":  config.Model,
		"input":  responsesInput(request.input),
		"stream": true,
	}

	if request.instructions != "" {
		payload["instructions"] = request.instructions
	}

	if request.previous != "" {
		payload["previous_response_id"] = request.previous
	}

	if len(request.turn.Tools) > 0 {
		payload["tools"] = responsesTools(request.turn.Tools)
	}

	if request.turn.ToolChoice != "" {
		payload["tool_choice"] = request.turn.ToolChoice
	}

	response, err := post(ctx, config, "/responses", payload)
	if err != nil {
		return reply, usage, "", err
	}

	defer func() { _ = response.Body.Close() }()

	var (
		responseID string
		finished   bool
	)

	err = sseData(response.Body, func(data string) (bool, error) {
		var event responsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return false, fmt.Errorf("模型接口返回了无法解析的事件: %s", snippet([]byte(data)))
		}

		if event.Response.ID != "" {
			responseID = event.Response.ID
		}

		switch event.Type {
		case "response.output_text.delta":
			reply.Content += event.Delta

			if onDelta != nil && event.Delta != "" {
				onDelta(event.Delta)
			}
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				reply.ToolCalls = append(reply.ToolCalls, ToolCall{
					ID:       event.Item.CallID,
					Type:     "function",
					Function: FunctionCall{Name: event.Item.Name, Arguments: event.Item.Arguments},
				})
			}
		case "response.completed":
			finished = true

			fillResponsesUsage(&usage, event)

			return true, nil
		case "response.incomplete":
			fillResponsesUsage(&usage, event)

			reason := "未知原因"

			if event.Response.IncompleteDetails != nil {
				reason = event.Response.IncompleteDetails.Reason
			}

			return true, fmt.Errorf("模型的回答不完整: %s", reason)
		case "response.failed":
			fillResponsesUsage(&usage, event)

			if event.Response.Error != nil {
				return true, fmt.Errorf("模型接口返回错误: %s", event.Response.Error.Message)
			}

			return true, errors.New("模型接口返回错误")
		case "error":
			return true, fmt.Errorf("模型接口返回错误: %s", event.Message)
		}

		return false, nil
	})
	if err != nil {
		return reply, usage, responseID, err
	}

	if !finished {
		return reply, usage, responseID, errors.New("模型接口的响应中途断开")
	}

	if reply.Content == "" && len(reply.ToolCalls) == 0 {
		return reply, usage, responseID, errors.New("模型接口没有返回内容")
	}

	return reply, usage, responseID, nil
}

// fillResponsesUsage 用 Responses 的用量字段填写 token 数
func fillResponsesUsage(usage *Usage, event responsesEvent) {
	if event.Response.Model != "" {
		usage.Model = event.Response.Model
	}

	reported := event.Response.Usage
	if reported == nil {
		return
	}

	usage.PromptTokens = reported.InputTokens
	usage.CachedTokens = reported.InputTokensDetails.CachedTokens
	usage.CompletionTokens = reported.OutputTokens
	usage.ReasoningTokens = reported.OutputTokensDetails.ReasoningTokens
	usage.TotalTokens = reported.TotalTokens

	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
}

// responsesInput 把消息换成 Responses 的输入项，工具结果写成 function_call_output
func responsesInput(messages []Message) []map[string]any {
	items := make([]map[string]any, 0, len(messages))

	for _, message := range messages {
		if message.Role == RoleTool {
			items = append(items, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})

			continue
		}

		items = append(items, map[string]any{"role": message.Role, "content": message.Content})
	}

	return items
}

// responsesTools 把工具定义换成 Responses 的扁平格式
func responsesTools(tools []Tool) []map[string]any {
	converted := make([]map[string]any, 0, len(tools))

	for _, tool := range tools {
		converted = append(converted, map[string]any{
			"type":        "function",
			"name":        tool.Function.Name,
			"description": tool.Function.Description,
			"parameters":  tool.Function.Parameters,
		})
	}

	return converted
}
