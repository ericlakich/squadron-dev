// Package mantle implements the provider.Provider interface against the Amazon
// Bedrock "bedrock-mantle" endpoint using the OpenAI-compatible Responses API:
//
//	POST https://bedrock-mantle.{region}.api.aws/v1/responses
//
// This is distinct from the sibling bedrock (bedrock-runtime) provider, which
// speaks the AWS Bedrock Converse API through the AWS SDK. The mantle endpoint is
// a plain HTTPS service that accepts the OpenAI Responses request/response format,
// so this provider talks to it directly with net/http and maps the neutral
// provider.Request/Response onto Responses items. No AWS SDK, SigV4, or Converse
// translation is involved.
//
// The agent loop is stateless — it resends the full conversation each turn — so
// this provider sends store=false and replays the history as Responses input
// items (message, function_call, function_call_output) rather than relying on
// previous_response_id.
//
// Authentication uses an Amazon Bedrock API key sent as an Authorization bearer
// token (the method the Responses API documents). The key comes from the
// bedrock_api_key setting — typically wired to a Squadron secret — falling back to
// the AWS_BEARER_TOKEN_BEDROCK or BEDROCK_API_KEY environment variable.
package mantle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ericlakich/squadron-dev/provider"
)

const (
	defaultRegion = "us-east-1"
	// defaultModelID is the model used when model_id is unset. The Responses API is
	// only supported by some models — VERIFY this matches a Responses-capable model
	// enabled on your account/region and override with model_id as needed.
	defaultModelID   = "openai.gpt-oss-120b"
	defaultMaxTokens = 8192
	// defaultTimeout is a backstop for a single inference turn; the caller's
	// context governs cancellation.
	defaultTimeout = 10 * time.Minute
	// maxResponseBytes caps how much of a response body we read so a misbehaving
	// endpoint cannot exhaust memory.
	maxResponseBytes = 32 << 20
	// responsesPath is appended to the endpoint base URL.
	responsesPath = "/v1/responses"
)

func init() {
	provider.Register("bedrock-mantle", New)
}

// Mantle is a provider backed by the bedrock-mantle Responses API.
type Mantle struct {
	http        *http.Client
	endpoint    string // full URL to POST
	apiKey      string
	modelID     string
	maxTokens   int32
	temperature float32
	setTemp     bool // whether to send a temperature at all (some models reject it)
}

// New builds a Mantle provider from settings.
//
// Recognized settings:
//   - aws_region:      region used to build the default endpoint host (default
//     us-east-1). Ignored when mantle_endpoint is set.
//   - mantle_endpoint: optional full base URL override, e.g.
//     https://bedrock-mantle.us-east-1.api.aws (the "/v1/responses" path is
//     appended if absent). Env fallback BEDROCK_MANTLE_ENDPOINT.
//   - bedrock_api_key: Amazon Bedrock API key sent as an Authorization bearer
//     token (usually a Squadron secret; env fallback AWS_BEARER_TOKEN_BEDROCK /
//     BEDROCK_API_KEY). Required.
//   - model_id:        Responses-capable model id (default openai.gpt-oss-120b).
//   - max_tokens:      max output tokens per turn -> max_output_tokens (default 8192).
//   - temperature:     sampling temperature. Sent only when set here or requested
//     per-call, since some Responses models reject an explicit temperature.
func New(settings map[string]string) (provider.Provider, error) {
	apiKey := firstNonEmpty(settings["bedrock_api_key"], os.Getenv("AWS_BEARER_TOKEN_BEDROCK"), os.Getenv("BEDROCK_API_KEY"))
	if apiKey == "" {
		return nil, fmt.Errorf("bedrock-mantle: an Amazon Bedrock API key is required (set the bedrock_api_key setting or AWS_BEARER_TOKEN_BEDROCK)")
	}

	region := get(settings, "aws_region", defaultRegion)
	base := firstNonEmpty(settings["mantle_endpoint"], os.Getenv("BEDROCK_MANTLE_ENDPOINT"))
	if base == "" {
		base = fmt.Sprintf("https://bedrock-mantle.%s.api.aws", region)
	}

	maxTokens := defaultMaxTokens
	if v := settings["max_tokens"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid max_tokens %q: must be a positive integer", v)
		}
		maxTokens = n
	}

	var temp float32
	setTemp := false
	if v := settings["temperature"]; v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid temperature %q: %w", v, err)
		}
		temp = float32(f)
		setTemp = true
	}

	return &Mantle{
		http:        &http.Client{Timeout: defaultTimeout},
		endpoint:    responsesURL(base),
		apiKey:      apiKey,
		modelID:     get(settings, "model_id", defaultModelID),
		maxTokens:   int32(maxTokens),
		temperature: temp,
		setTemp:     setTemp,
	}, nil
}

// responsesURL normalizes a base URL to the full Responses URL, tolerating a bare
// host or a URL that already includes the responses path.
func responsesURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, responsesPath) {
		return base
	}
	return base + responsesPath
}

// Name implements provider.Provider.
func (m *Mantle) Name() string { return "bedrock-mantle" }

// Converse implements provider.Provider by mapping the neutral request onto a
// Responses request, POSTing it to the mantle endpoint, and mapping the result
// back.
func (m *Mantle) Converse(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	maxTokens := m.maxTokens
	if req.MaxTokens > 0 {
		maxTokens = int32(req.MaxTokens)
	}

	body := wireRequest{
		Model:           m.modelID,
		Instructions:    req.System,
		Input:           toInput(req.Messages),
		Tools:           toWireTools(req.Tools),
		MaxOutputTokens: maxTokens,
		Store:           false,
	}
	if temp, ok := m.effectiveTemperature(req); ok {
		body.Temperature = &temp
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock-mantle: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("bedrock-mantle: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock-mantle: post %s: %w", m.endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("bedrock-mantle: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bedrock-mantle (model %s): %s", m.modelID, describeError(resp.StatusCode, respBody))
	}
	return parseResponse(m.modelID, respBody)
}

// effectiveTemperature returns the temperature to send and whether to send one at
// all. A per-call override (>0) always applies; otherwise the configured value is
// sent only if a temperature setting was provided.
func (m *Mantle) effectiveTemperature(req *provider.Request) (float32, bool) {
	if req.Temperature > 0 {
		return req.Temperature, true
	}
	return m.temperature, m.setTemp
}

// --- OpenAI Responses wire types -------------------------------------------

type wireRequest struct {
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions,omitempty"`
	Input           []any      `json:"input"`
	Tools           []wireTool `json:"tools,omitempty"`
	MaxOutputTokens int32      `json:"max_output_tokens"`
	Temperature     *float32   `json:"temperature,omitempty"`
	Store           bool       `json:"store"`
}

// Input item types. Each marshals exactly its own fields.
type messageItem struct {
	Type    string        `json:"type"` // "message"
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type string `json:"type"` // "input_text" (user) or "output_text" (assistant)
	Text string `json:"text"`
}

type functionCallItem struct {
	Type      string `json:"type"` // "function_call"
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded object, as a string
}

type functionCallOutputItem struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// wireTool is a Responses "function" tool (flat form: name/description/parameters
// at the top level alongside type).
type wireTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireResponse struct {
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

type wireError struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// toInput replays the neutral conversation as Responses input items. The agent
// loop is stateless, so the full history is sent every turn.
func toInput(msgs []provider.Message) []any {
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
				items = append(items, functionCallItem{
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
				items = append(items, functionCallOutputItem{
					Type:   "function_call_output",
					CallID: blk.ToolResult.ToolUseID,
					Output: out,
				})
			default:
				items = append(items, messageItem{
					Type:    "message",
					Role:    role,
					Content: []contentPart{{Type: partType, Text: blk.Text}},
				})
			}
		}
	}
	return items
}

func toWireTools(specs []provider.ToolSpec) []wireTool {
	if len(specs) == 0 {
		return nil
	}
	tools := make([]wireTool, 0, len(specs))
	for _, s := range specs {
		params := s.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, wireTool{
			Type:        "function",
			Name:        s.Name,
			Description: s.Description,
			Parameters:  params,
		})
	}
	return tools
}

func parseResponse(modelID string, body []byte) (*provider.Response, error) {
	var wr wireResponse
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
	resp.StopReason = stopReason(&wr, len(resp.ToolUses) > 0)
	return resp, nil
}

// stopReason maps the Responses status onto the neutral stop reason. The agent
// loop treats StopToolUse (with tool calls present) as "keep going", so tool calls
// take precedence over the status.
func stopReason(wr *wireResponse, hasToolCalls bool) provider.StopReason {
	if hasToolCalls {
		return provider.StopToolUse
	}
	if wr.Status == "incomplete" && wr.IncompleteDetails != nil && wr.IncompleteDetails.Reason == "max_output_tokens" {
		return provider.StopMaxTokens
	}
	return provider.StopEndTurn
}

// describeError renders a non-2xx response. It reads the structured error message
// when present and otherwise a bounded slice of the raw body. The API key lives
// only in the request header, never the body, so nothing here leaks the secret.
func describeError(status int, body []byte) string {
	var we wireError
	if err := json.Unmarshal(body, &we); err == nil && we.Error.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", status, we.Error.Message)
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, msg)
}

// argsString renders a tool-use input (a JSON object) as the string the Responses
// API expects for function_call.arguments.
func argsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// rawArgs parses a function_call.arguments string (JSON text) into a RawMessage,
// defaulting to an empty object when absent or invalid.
func rawArgs(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage(`{}`)
	}
	if !json.Valid([]byte(s)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func get(m map[string]string, key, def string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return def
}
