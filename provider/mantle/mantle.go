// Package mantle implements the provider.Provider interface against the Amazon
// Bedrock "bedrock-mantle" endpoint. The mantle endpoint exposes several
// OpenAI/Anthropic-compatible APIs; this provider supports two, selected by the
// mantle_api setting:
//
//   - "responses" (default): the OpenAI-compatible Responses API
//     (POST https://bedrock-mantle.{region}.api.aws/v1/responses). Only a subset
//     of models support it (the OpenAI GPT family).
//   - "chat_completions": the OpenAI-compatible Chat Completions API
//     (POST https://bedrock-mantle.{region}.api.aws/v1/chat/completions). Broadly
//     supported, including Qwen, which does not support the Responses API.
//
// This is distinct from the sibling bedrock (bedrock-runtime) provider, which
// speaks the AWS Bedrock Converse API through the AWS SDK. The mantle endpoint is a
// plain HTTPS service, so this provider talks to it directly with net/http and maps
// the neutral provider.Request/Response onto the selected API's wire format. No AWS
// SDK, SigV4, or Converse translation is involved.
//
// The agent loop is stateless — it resends the full conversation each turn — so
// both API mappings replay the full history rather than relying on server-side
// state (the Responses API is sent with store=false).
//
// Authentication uses an Amazon Bedrock API key sent as an Authorization bearer
// token. The key comes from the bedrock_api_key setting — typically wired to a
// Squadron secret — falling back to the AWS_BEARER_TOKEN_BEDROCK or BEDROCK_API_KEY
// environment variable.
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
	// defaultModelID is the model used when model_id is unset. The default suits the
	// Responses API; for chat_completions set model_id to a Chat-Completions-capable
	// model (e.g. qwen.qwen3-coder-next). Always VERIFY the id/API against your
	// account and region.
	defaultModelID   = "openai.gpt-oss-120b"
	defaultMaxTokens = 8192
	// defaultTimeout is a backstop for a single inference turn; the caller's
	// context governs cancellation.
	defaultTimeout = 10 * time.Minute
	// maxResponseBytes caps how much of a response body we read so a misbehaving
	// endpoint cannot exhaust memory.
	maxResponseBytes = 32 << 20

	// mantle_api modes and their endpoint paths.
	apiResponses       = "responses"
	apiChatCompletions = "chat_completions"
	responsesPath      = "/v1/responses"
	chatPath           = "/v1/chat/completions"
)

func init() {
	provider.Register("bedrock-mantle", New)
}

// Mantle is a provider backed by the bedrock-mantle endpoint.
type Mantle struct {
	http        *http.Client
	endpoint    string // full URL to POST (path depends on api)
	api         string // apiResponses or apiChatCompletions
	apiKey      string
	modelID     string
	maxTokens   int32
	temperature float32
	setTemp     bool // whether to send a temperature at all (some models reject it)
}

// New builds a Mantle provider from settings.
//
// Recognized settings:
//   - mantle_api:      "responses" (default) or "chat_completions". Chat Completions
//     is required for models that don't support the Responses API (e.g. Qwen).
//   - aws_region:      region used to build the default endpoint host (default
//     us-east-1). Ignored when mantle_endpoint is set.
//   - mantle_endpoint: optional full base URL override, e.g.
//     https://bedrock-mantle.us-east-1.api.aws (the API path is appended if absent).
//     Env fallback BEDROCK_MANTLE_ENDPOINT.
//   - bedrock_api_key: Amazon Bedrock API key sent as an Authorization bearer token
//     (usually a Squadron secret; env fallback AWS_BEARER_TOKEN_BEDROCK /
//     BEDROCK_API_KEY). Required.
//   - model_id:        model id (default openai.gpt-oss-120b).
//   - max_tokens:      max output tokens per turn (default 8192).
//   - temperature:     sampling temperature. Sent only when set here or requested
//     per-call, since some models reject an explicit temperature.
func New(settings map[string]string) (provider.Provider, error) {
	api := get(settings, "mantle_api", apiResponses)
	switch api {
	case apiResponses, apiChatCompletions:
	case "chat": // convenience alias
		api = apiChatCompletions
	default:
		return nil, fmt.Errorf("invalid mantle_api %q: must be %q or %q", api, apiResponses, apiChatCompletions)
	}

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
		endpoint:    endpointURL(base, apiPath(api)),
		api:         api,
		apiKey:      apiKey,
		modelID:     get(settings, "model_id", defaultModelID),
		maxTokens:   int32(maxTokens),
		temperature: temp,
		setTemp:     setTemp,
	}, nil
}

// apiPath returns the HTTP path for the selected API mode.
func apiPath(api string) string {
	if api == apiChatCompletions {
		return chatPath
	}
	return responsesPath
}

// endpointURL joins a base URL with an API path, tolerating a bare host or a URL
// that already includes the path.
func endpointURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, path) {
		return base
	}
	return base + path
}

// Name implements provider.Provider.
func (m *Mantle) Name() string { return "bedrock-mantle" }

// Converse implements provider.Provider, dispatching to the configured API.
func (m *Mantle) Converse(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	if m.api == apiChatCompletions {
		return m.converseChat(ctx, req)
	}
	return m.converseResponses(ctx, req)
}

// postJSON marshals body, POSTs it to the endpoint with auth headers, and returns
// the response body, converting a non-2xx status into an error.
func (m *Mantle) postJSON(ctx context.Context, body any) ([]byte, error) {
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
	return respBody, nil
}

// effectiveMaxTokens returns the max output tokens for a request (per-call override
// wins over the configured default).
func (m *Mantle) effectiveMaxTokens(req *provider.Request) int32 {
	if req.MaxTokens > 0 {
		return int32(req.MaxTokens)
	}
	return m.maxTokens
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

type wireError struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
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

// argsString renders a tool-use input (a JSON object) as the string the OpenAI
// wire formats expect for function-call arguments.
func argsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// rawArgs parses a function-call arguments string (JSON text) into a RawMessage,
// defaulting to an empty object when absent or invalid.
func rawArgs(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" || !json.Valid([]byte(s)) {
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
