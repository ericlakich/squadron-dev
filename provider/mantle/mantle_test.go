package mantle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericlakich/squadron-dev/provider"
)

func TestProviderRegistered(t *testing.T) {
	found := false
	for _, n := range provider.Available() {
		if n == "bedrock-mantle" {
			found = true
		}
	}
	if !found {
		t.Fatal("bedrock-mantle provider was not registered")
	}
}

func TestResponsesURL(t *testing.T) {
	cases := map[string]string{
		"https://bedrock-mantle.us-east-1.api.aws":              "https://bedrock-mantle.us-east-1.api.aws/v1/responses",
		"https://bedrock-mantle.us-east-1.api.aws/":             "https://bedrock-mantle.us-east-1.api.aws/v1/responses",
		"https://bedrock-mantle.us-east-1.api.aws/v1/responses": "https://bedrock-mantle.us-east-1.api.aws/v1/responses",
		"https://gw.example.com/proxy":                          "https://gw.example.com/proxy/v1/responses",
	}
	for in, want := range cases {
		if got := responsesURL(in); got != want {
			t.Errorf("responsesURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("BEDROCK_API_KEY", "")
	if _, err := New(map[string]string{"aws_region": "us-east-1"}); err == nil {
		t.Error("expected error when no API key is provided")
	}
}

func TestNewBuildsRegionalEndpoint(t *testing.T) {
	p, err := New(map[string]string{"aws_region": "us-west-2", "bedrock_api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*Mantle).endpoint; got != "https://bedrock-mantle.us-west-2.api.aws/v1/responses" {
		t.Errorf("endpoint = %q", got)
	}
}

func TestNewFallsBackToEnv(t *testing.T) {
	t.Setenv("BEDROCK_MANTLE_ENDPOINT", "https://env.example.com")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "env-token")
	p, err := New(map[string]string{})
	if err != nil {
		t.Fatalf("New with env fallbacks: %v", err)
	}
	m := p.(*Mantle)
	if m.endpoint != "https://env.example.com/v1/responses" {
		t.Errorf("endpoint = %q", m.endpoint)
	}
	if m.apiKey != "env-token" {
		t.Errorf("apiKey = %q, want env-token", m.apiKey)
	}
}

func TestToInputItemShapes(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("do the thing")}},
		{Role: provider.RoleAssistant, Blocks: []provider.Block{
			provider.TextBlock("on it"),
			{ToolUse: &provider.ToolUse{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)}},
		}},
		{Role: provider.RoleUser, Blocks: []provider.Block{
			{ToolResult: &provider.ToolResult{ToolUseID: "call_1", Content: ""}},
		}},
	}
	b, err := json.Marshal(toInput(msgs))
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d input items, want 4", len(items))
	}

	// user message -> input_text
	if items[0]["type"] != "message" || items[0]["role"] != "user" {
		t.Errorf("item0 = %v", items[0])
	}
	if part := items[0]["content"].([]any)[0].(map[string]any); part["type"] != "input_text" || part["text"] != "do the thing" {
		t.Errorf("item0 content = %v", part)
	}

	// assistant text -> output_text
	if part := items[1]["content"].([]any)[0].(map[string]any); part["type"] != "output_text" {
		t.Errorf("item1 content type = %v, want output_text", part["type"])
	}

	// assistant tool call -> function_call with call_id + arguments as a JSON string
	if items[2]["type"] != "function_call" || items[2]["call_id"] != "call_1" || items[2]["name"] != "read_file" {
		t.Errorf("item2 = %v", items[2])
	}
	if items[2]["arguments"] != `{"path":"a.go"}` {
		t.Errorf("item2 arguments = %v, want a JSON string", items[2]["arguments"])
	}

	// tool result -> function_call_output with placeholder for empty content
	if items[3]["type"] != "function_call_output" || items[3]["call_id"] != "call_1" {
		t.Errorf("item3 = %v", items[3])
	}
	if items[3]["output"] != "(no output)" {
		t.Errorf("item3 output = %v, want placeholder", items[3]["output"])
	}
}

func TestToWireToolsFlatFunctionForm(t *testing.T) {
	tools := toWireTools([]provider.ToolSpec{
		{Name: "read_file", Description: "reads", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
		{Name: "noargs"},
	})
	if len(tools) != 2 {
		t.Fatalf("got %d tools", len(tools))
	}
	if tools[0].Type != "function" || tools[0].Name != "read_file" {
		t.Errorf("tool0 = %+v", tools[0])
	}
	if string(tools[1].Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("default params = %s", tools[1].Parameters)
	}
}

func TestParseResponseTextAndToolCall(t *testing.T) {
	body := []byte(`{
		"status":"completed",
		"output":[
			{"type":"reasoning","content":[]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"here goes"}]},
			{"type":"function_call","id":"fc_1","call_id":"call_9","name":"run","arguments":"{\"cmd\":\"go test\"}"}
		],
		"usage":{"input_tokens":7,"output_tokens":13}
	}`)
	resp, err := parseResponse("m", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "here goes" {
		t.Errorf("text = %q", resp.Text)
	}
	if resp.StopReason != provider.StopToolUse {
		t.Errorf("stop reason = %v, want tool_use", resp.StopReason)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].ID != "call_9" || resp.ToolUses[0].Name != "run" {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	if string(resp.ToolUses[0].Input) != `{"cmd":"go test"}` {
		t.Errorf("tool input = %s", resp.ToolUses[0].Input)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 13 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestParseResponseStopReasons(t *testing.T) {
	completed := []byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`)
	if resp, _ := parseResponse("m", completed); resp.StopReason != provider.StopEndTurn {
		t.Errorf("completed stop = %v, want end_turn", resp.StopReason)
	}
	truncated := []byte(`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"par"}]}]}`)
	if resp, _ := parseResponse("m", truncated); resp.StopReason != provider.StopMaxTokens {
		t.Errorf("truncated stop = %v, want max_tokens", resp.StopReason)
	}
}

func TestParseResponseFailedStatus(t *testing.T) {
	body := []byte(`{"status":"failed","error":{"code":"server_error","message":"boom"}}`)
	if _, err := parseResponse("m", body); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected failed-status error mentioning boom, got %v", err)
	}
}

func TestDescribeError(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","message":"bad model"}}`)
	if got := describeError(400, body); got != "HTTP 400: bad model" {
		t.Errorf("describeError = %q", got)
	}
	if got := describeError(500, []byte("upstream boom")); got != "HTTP 500: upstream boom" {
		t.Errorf("describeError fallback = %q", got)
	}
}

// TestConverseRoundTrip drives a full Converse against an httptest server that
// impersonates the mantle Responses endpoint, verifying headers, path, request
// mapping (instructions/store/tools), and response mapping.
func TestConverseRoundTrip(t *testing.T) {
	var gotAuth, gotPath string
	var gotReq wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi back"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer srv.Close()

	p, err := New(map[string]string{
		"mantle_endpoint": srv.URL,
		"bedrock_api_key": "test-key",
		"model_id":        "openai.gpt-oss-120b",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Converse(context.Background(), &provider.Request{
		System:   "be helpful",
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
		Tools:    []provider.ToolSpec{{Name: "t", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}
	if gotPath != "/v1/responses" {
		t.Errorf("path = %q, want /v1/responses", gotPath)
	}
	if gotReq.Model != "openai.gpt-oss-120b" || gotReq.Instructions != "be helpful" {
		t.Errorf("request model/instructions = %q / %q", gotReq.Model, gotReq.Instructions)
	}
	if gotReq.Store != false {
		t.Errorf("store = %v, want false", gotReq.Store)
	}
	if len(gotReq.Tools) != 1 || gotReq.Tools[0].Type != "function" {
		t.Errorf("tools = %+v", gotReq.Tools)
	}
	if resp.Text != "hi back" || resp.StopReason != provider.StopEndTurn {
		t.Errorf("resp = %+v", resp)
	}
}

func TestConverseOmitsTemperatureByDefault(t *testing.T) {
	var rawReq []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawReq, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"status":"completed","output":[]}`)
	}))
	defer srv.Close()

	// No temperature setting -> no temperature field in the request body.
	p, _ := New(map[string]string{"mantle_endpoint": srv.URL, "bedrock_api_key": "k"})
	_, err := p.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawReq), "temperature") {
		t.Errorf("request should omit temperature when unset: %s", rawReq)
	}

	// temperature setting present -> field is sent (even when 0).
	p2, _ := New(map[string]string{"mantle_endpoint": srv.URL, "bedrock_api_key": "k", "temperature": "0"})
	_, err = p2.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawReq), `"temperature":0`) {
		t.Errorf("request should include temperature when set: %s", rawReq)
	}
}

func TestConverseSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()

	p, err := New(map[string]string{"mantle_endpoint": srv.URL, "bedrock_api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	})
	if err == nil {
		t.Fatal("expected an error from a 429 response")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "slow down") {
		t.Errorf("error = %q, want it to mention 429 and the message", err)
	}
}
