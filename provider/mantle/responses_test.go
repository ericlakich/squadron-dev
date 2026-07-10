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

func TestToResponsesInputItemShapes(t *testing.T) {
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
	b, err := json.Marshal(toResponsesInput(msgs))
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
	if part := items[0]["content"].([]any)[0].(map[string]any); part["type"] != "input_text" || part["text"] != "do the thing" {
		t.Errorf("item0 content = %v", part)
	}
	if part := items[1]["content"].([]any)[0].(map[string]any); part["type"] != "output_text" {
		t.Errorf("item1 content type = %v, want output_text", part["type"])
	}
	if items[2]["type"] != "function_call" || items[2]["call_id"] != "call_1" || items[2]["arguments"] != `{"path":"a.go"}` {
		t.Errorf("item2 = %v", items[2])
	}
	if items[3]["type"] != "function_call_output" || items[3]["call_id"] != "call_1" || items[3]["output"] != "(no output)" {
		t.Errorf("item3 = %v", items[3])
	}
}

func TestToResponsesToolsFlatForm(t *testing.T) {
	tools := toResponsesTools([]provider.ToolSpec{{Name: "noargs"}})
	if len(tools) != 1 || tools[0].Type != "function" {
		t.Fatalf("tools = %+v", tools)
	}
	if string(tools[0].Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("default params = %s", tools[0].Parameters)
	}
}

func TestParseResponsesTextAndToolCall(t *testing.T) {
	body := []byte(`{
		"status":"completed",
		"output":[
			{"type":"reasoning","content":[]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"here goes"}]},
			{"type":"function_call","id":"fc_1","call_id":"call_9","name":"run","arguments":"{\"cmd\":\"go test\"}"}
		],
		"usage":{"input_tokens":7,"output_tokens":13}
	}`)
	resp, err := parseResponsesResponse("m", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "here goes" || resp.StopReason != provider.StopToolUse {
		t.Errorf("text/stop = %q / %v", resp.Text, resp.StopReason)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].ID != "call_9" || string(resp.ToolUses[0].Input) != `{"cmd":"go test"}` {
		t.Errorf("tool uses = %+v", resp.ToolUses)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 13 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestParseResponsesStopReasons(t *testing.T) {
	completed := []byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`)
	if resp, _ := parseResponsesResponse("m", completed); resp.StopReason != provider.StopEndTurn {
		t.Errorf("completed stop = %v, want end_turn", resp.StopReason)
	}
	truncated := []byte(`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"par"}]}]}`)
	if resp, _ := parseResponsesResponse("m", truncated); resp.StopReason != provider.StopMaxTokens {
		t.Errorf("truncated stop = %v, want max_tokens", resp.StopReason)
	}
}

func TestParseResponsesFailedStatus(t *testing.T) {
	body := []byte(`{"status":"failed","error":{"code":"server_error","message":"boom"}}`)
	if _, err := parseResponsesResponse("m", body); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected failed-status error mentioning boom, got %v", err)
	}
}

// terminalResponsesEvent is a response.completed SSE payload carrying the given
// assistant text.
func terminalResponsesEvent(text string) string {
	return `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}],"usage":{"input_tokens":1,"output_tokens":2}}}`
}

func TestResponsesStreamAccumulator(t *testing.T) {
	acc := &responsesStreamAccumulator{}
	// Delta/created events are ignored; only the terminal event is captured.
	_ = acc.add([]byte(`{"type":"response.created","response":{"status":"in_progress"}}`))
	_ = acc.add([]byte(`{"type":"response.output_text.delta","delta":"hi"}`))
	_ = acc.add([]byte(terminalResponsesEvent("done")))
	if len(acc.final) == 0 {
		t.Fatal("terminal event not captured")
	}
	resp, err := parseResponsesResponse("m", acc.final)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "done" || resp.StopReason != provider.StopEndTurn {
		t.Errorf("resp = %+v", resp)
	}
}

// TestConverseResponsesStreamRoundTrip drives the Responses provider end to end
// against an SSE server, verifying headers, path, streaming request, and mapping.
func TestConverseResponsesStreamRoundTrip(t *testing.T) {
	var gotAuth, gotPath string
	var gotReq responsesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotReq)
		writeSSE(w,
			`{"type":"response.created","response":{"status":"in_progress"}}`,
			`{"type":"response.output_text.delta","delta":"hi "}`,
			terminalResponsesEvent("hi back"),
			"[DONE]",
		)
	}))
	defer srv.Close()

	p, err := New(map[string]string{"mantle_endpoint": srv.URL, "bedrock_api_key": "test-key", "model_id": "openai.gpt-oss-120b"})
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
	if gotAuth != "Bearer test-key" || gotPath != "/v1/responses" {
		t.Errorf("auth/path = %q / %q", gotAuth, gotPath)
	}
	if gotReq.Model != "openai.gpt-oss-120b" || gotReq.Instructions != "be helpful" || !gotReq.Stream || gotReq.Store {
		t.Errorf("request = %+v", gotReq)
	}
	if resp.Text != "hi back" || resp.StopReason != provider.StopEndTurn {
		t.Errorf("resp = %+v", resp)
	}
}

func TestConverseResponsesOmitsTemperatureByDefault(t *testing.T) {
	var rawReq []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawReq, _ = io.ReadAll(r.Body)
		writeSSE(w, terminalResponsesEvent("ok"), "[DONE]")
	}))
	defer srv.Close()

	p, _ := New(map[string]string{"mantle_endpoint": srv.URL, "bedrock_api_key": "k"})
	if _, err := p.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawReq), "temperature") {
		t.Errorf("request should omit temperature when unset: %s", rawReq)
	}

	p2, _ := New(map[string]string{"mantle_endpoint": srv.URL, "bedrock_api_key": "k", "temperature": "0"})
	if _, err := p2.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	}); err != nil {
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

	p, _ := New(map[string]string{"mantle_endpoint": srv.URL, "bedrock_api_key": "k"})
	_, err := p.Converse(context.Background(), &provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "slow down") {
		t.Errorf("error = %v, want 429 + message", err)
	}
}
