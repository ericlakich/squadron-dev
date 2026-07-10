package mantle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericlakich/squadron-dev/provider"
)

// converseChat runs one streaming turn against the OpenAI-compatible Chat
// Completions API. This is the path for models that support Chat Completions but
// not the Responses API (e.g. Qwen). Streaming feeds the stall watchdog; the
// delta chunks are reassembled into a single response.
func (m *Mantle) converseChat(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body := chatRequest{
		Model:         m.modelID,
		Messages:      toChatMessages(req.System, req.Messages),
		Tools:         toChatTools(req.Tools),
		MaxTokens:     m.effectiveMaxTokens(req),
		Stream:        true,
		StreamOptions: &chatStreamOptions{IncludeUsage: true},
	}
	if temp, ok := m.effectiveTemperature(req); ok {
		body.Temperature = &temp
	}

	acc := &chatStreamAccumulator{}
	if err := m.stream(ctx, body, acc.add); err != nil {
		return nil, err
	}
	return acc.result(), nil
}

type chatRequest struct {
	Model         string             `json:"model"`
	Messages      []chatMessage      `json:"messages"`
	Tools         []chatTool         `json:"tools,omitempty"`
	MaxTokens     int32              `json:"max_tokens,omitempty"`
	Temperature   *float32           `json:"temperature,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`
}

// chatStreamOptions asks the endpoint to include a final usage chunk in the
// stream (OpenAI omits usage from streamed responses otherwise).
type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatStreamAccumulator reassembles Chat Completions delta chunks: content is
// concatenated, tool calls are assembled by index (id/name from the first chunk,
// argument fragments appended), and finish_reason / usage come from later chunks.
type chatStreamAccumulator struct {
	content strings.Builder
	calls   map[int]*chatCallAccumulator
	order   []int
	finish  string
	inTok   int
	outTok  int
}

type chatCallAccumulator struct {
	id   string
	name string
	args strings.Builder
}

func (a *chatStreamAccumulator) add(data []byte) error {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return fmt.Errorf("bedrock-mantle: decode chat chunk: %w", err)
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		return fmt.Errorf("bedrock-mantle: %s", chunk.Error.Message)
	}
	if chunk.Usage != nil {
		a.inTok = chunk.Usage.PromptTokens
		a.outTok = chunk.Usage.CompletionTokens
	}
	for _, ch := range chunk.Choices {
		a.content.WriteString(ch.Delta.Content)
		if ch.FinishReason != "" {
			a.finish = ch.FinishReason
		}
		for _, tc := range ch.Delta.ToolCalls {
			if a.calls == nil {
				a.calls = map[int]*chatCallAccumulator{}
			}
			c := a.calls[tc.Index]
			if c == nil {
				c = &chatCallAccumulator{}
				a.calls[tc.Index] = c
				a.order = append(a.order, tc.Index)
			}
			if tc.ID != "" {
				c.id = tc.ID
			}
			if tc.Function.Name != "" {
				c.name = tc.Function.Name
			}
			c.args.WriteString(tc.Function.Arguments)
		}
	}
	return nil
}

func (a *chatStreamAccumulator) result() *provider.Response {
	resp := &provider.Response{Text: a.content.String()}
	resp.Usage.InputTokens = a.inTok
	resp.Usage.OutputTokens = a.outTok
	for _, idx := range a.order {
		c := a.calls[idx]
		resp.ToolUses = append(resp.ToolUses, provider.ToolUse{
			ID:    c.id,
			Name:  c.name,
			Input: rawArgs(c.args.String()),
		})
	}
	resp.StopReason = chatStopReason(a.finish, len(resp.ToolUses) > 0)
	return resp
}

type chatMessage struct {
	Role      string         `json:"role"` // system | user | assistant | tool
	Content   string         `json:"content,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a role:"tool" message to the assistant tool call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function chatToolCallFunc `json:"function"`
}

type chatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded object, as a string
}

// chatTool is a Chat Completions "function" tool (nested form: the function
// definition is under a "function" key).
type chatTool struct {
	Type     string      `json:"type"` // "function"
	Function chatToolDef `json:"function"`
}

type chatToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toChatMessages maps the system prompt and neutral conversation onto Chat
// Completions messages. An assistant turn's text and tool calls collapse into a
// single assistant message; each tool result becomes a separate role:"tool"
// message keyed by the tool call id.
func toChatMessages(system string, msgs []provider.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs)+1)
	if system != "" {
		out = append(out, chatMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant {
			var text []string
			var calls []chatToolCall
			for _, blk := range m.Blocks {
				if blk.ToolUse != nil {
					calls = append(calls, chatToolCall{
						ID:       blk.ToolUse.ID,
						Type:     "function",
						Function: chatToolCallFunc{Name: blk.ToolUse.Name, Arguments: argsString(blk.ToolUse.Input)},
					})
				} else if blk.ToolResult == nil && blk.Text != "" {
					text = append(text, blk.Text)
				}
			}
			out = append(out, chatMessage{Role: "assistant", Content: strings.Join(text, "\n"), ToolCalls: calls})
			continue
		}
		for _, blk := range m.Blocks {
			switch {
			case blk.ToolResult != nil:
				content := blk.ToolResult.Content
				if content == "" {
					content = "(no output)"
				}
				out = append(out, chatMessage{Role: "tool", ToolCallID: blk.ToolResult.ToolUseID, Content: content})
			case blk.ToolUse != nil:
				// user messages don't carry tool-use blocks; ignore defensively.
			default:
				out = append(out, chatMessage{Role: "user", Content: blk.Text})
			}
		}
	}
	return out
}

func toChatTools(specs []provider.ToolSpec) []chatTool {
	if len(specs) == 0 {
		return nil
	}
	tools := make([]chatTool, 0, len(specs))
	for _, s := range specs {
		params := s.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, chatTool{
			Type:     "function",
			Function: chatToolDef{Name: s.Name, Description: s.Description, Parameters: params},
		})
	}
	return tools
}

// chatStopReason maps the Chat Completions finish_reason onto the neutral stop
// reason. Tool calls take precedence so the agent loop keeps going.
func chatStopReason(r string, hasToolCalls bool) provider.StopReason {
	if hasToolCalls {
		return provider.StopToolUse
	}
	switch r {
	case "tool_calls":
		return provider.StopToolUse
	case "length":
		return provider.StopMaxTokens
	case "stop":
		return provider.StopEndTurn
	default:
		return provider.StopEndTurn
	}
}
