package mantle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericlakich/squadron-dev/provider"
)

// converseResponses runs one turn against the OpenAI-compatible Responses API.
func (m *Mantle) converseResponses(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body := responsesRequest{
		Model:           m.modelID,
		Instructions:    req.System,
		Input:           toResponsesInput(req.Messages),
		Tools:           toResponsesTools(req.Tools),
		MaxOutputTokens: m.effectiveMaxTokens(req),
		Store:           false,
	}
	if temp, ok := m.effectiveTemperature(req); ok {
		body.Temperature = &temp
	}
	respBody, err := m.postJSON(ctx, body)
	if err != nil {
		return nil, err
	}
	return parseResponsesResponse(m.modelID, respBody)
}

type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []any           `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens int32           `json:"max_output_tokens"`
	Temperature     *float32        `json:"temperature,omitempty"`
	Store           bool            `json:"store"`
}

// Input item types. Each marshals exactly its own fields.
type responsesMessageItem struct {
	Type    string                 `json:"type"` // "message"
	Role    string                 `json:"role"`
	Content []responsesContentPart `json:"content"`
}

type responsesContentPart struct {
	Type string `json:"type"` // "input_text" (user) or "output_text" (assistant)
	Text string `json:"text"`
}

type responsesFunctionCallItem struct {
	Type      string `json:"type"` // "function_call"
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded object, as a string
}

type responsesFunctionCallOutputItem struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// responsesTool is a Responses "function" tool (flat form: name/description/
// parameters at the top level alongside type).
type responsesTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responsesResponse struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// toResponsesInput replays the neutral conversation as Responses input items. The
// agent loop is stateless, so the full history is sent every turn.
func toResponsesInput(msgs []provider.Message) []any {
	items := make([]any, 0, len(msgs))
	for _, m := range msgs {
		partType := "input_text"
		role := "user"
		if m.Role == provider.RoleAssistant {
			partType = "output_text"
			role = "assistant"
		}
		for _, blk := range m.Blocks {
			switch {
			case blk.ToolUse != nil:
				items = append(items, responsesFunctionCallItem{
					Type:      "function_call",
					CallID:    blk.ToolUse.ID,
					Name:      blk.ToolUse.Name,
					Arguments: argsString(blk.ToolUse.Input),
				})
			case blk.ToolResult != nil:
				out := blk.ToolResult.Content
				if out == "" {
					out = "(no output)"
				}
				items = append(items, responsesFunctionCallOutputItem{
					Type:   "function_call_output",
					CallID: blk.ToolResult.ToolUseID,
					Output: out,
				})
			default:
				items = append(items, responsesMessageItem{
					Type:    "message",
					Role:    role,
					Content: []responsesContentPart{{Type: partType, Text: blk.Text}},
				})
			}
		}
	}
	return items
}

func toResponsesTools(specs []provider.ToolSpec) []responsesTool {
	if len(specs) == 0 {
		return nil
	}
	tools := make([]responsesTool, 0, len(specs))
	for _, s := range specs {
		params := s.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        s.Name,
			Description: s.Description,
			Parameters:  params,
		})
	}
	return tools
}

func parseResponsesResponse(modelID string, body []byte) (*provider.Response, error) {
	var wr responsesResponse
	if err := json.Unmarshal(body, &wr); err != nil {
		return nil, fmt.Errorf("bedrock-mantle: decode response: %w", err)
	}
	if wr.Status == "failed" && wr.Error != nil {
		return nil, fmt.Errorf("bedrock-mantle (model %s): response failed: %s", modelID, wr.Error.Message)
	}

	resp := &provider.Response{}
	resp.Usage.InputTokens = wr.Usage.InputTokens
	resp.Usage.OutputTokens = wr.Usage.OutputTokens

	var textParts []string
	for _, item := range wr.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					textParts = append(textParts, c.Text)
				}
			}
		case "function_call":
			resp.ToolUses = append(resp.ToolUses, provider.ToolUse{
				ID:    item.CallID,
				Name:  item.Name,
				Input: rawArgs(item.Arguments),
			})
		}
	}
	resp.Text = strings.Join(textParts, "\n")
	resp.StopReason = responsesStopReason(&wr, len(resp.ToolUses) > 0)
	return resp, nil
}

// responsesStopReason maps the Responses status onto the neutral stop reason. The
// agent loop treats StopToolUse (with tool calls present) as "keep going", so tool
// calls take precedence over the status.
func responsesStopReason(wr *responsesResponse, hasToolCalls bool) provider.StopReason {
	if hasToolCalls {
		return provider.StopToolUse
	}
	if wr.Status == "incomplete" && wr.IncompleteDetails != nil && wr.IncompleteDetails.Reason == "max_output_tokens" {
		return provider.StopMaxTokens
	}
	return provider.StopEndTurn
}
