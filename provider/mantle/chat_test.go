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

// writeSSE streams the given data payloads as Server-Sent Events, flushing each.
func writeSSE(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	for _, p := range payloads {
		io.WriteString(w, "data: "+p+"\n\n")
		if fl != nil {
			fl.Flush()
		}
	}
}

func TestToChatMessagesShapes(t *testing.T) {
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
	b, _ := json.Marshal(toChatMessages("be helpful", msgs))
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d messages, want 4: %s", len(items), b)
	}
	if items[0]["role"] != "system" || items[0]["content"] != "be helpful" {
		t.Errorf("system msg = %v", items[0])
	}
	if items[1]["role"] != "user" || items[1]["content"] != "do the thing" {
		t.Errorf("user msg = %v", items[1])
	}
	asst := items[2]
	if asst["role"] != "assistant" || asst["content"] != "on it" {
		t.Errorf("assistant msg = %v", asst)
	}
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if tc["id"] != "call_1" || tc["type"] != "function" || fn["name"] != "read_file" || fn["arguments"] != `{"path":"a.go"}` {
		t.Errorf("tool_call = %v", tc)
	}
	tool := items[3]
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "(no output)" {
		t.Errorf("tool msg = %v", tool)
	}
}

func TestToChatToolsNestedForm(t *testing.T) {
	tools := toChatTools([]provider.ToolSpec{
		{Name: "read_file", Description: "reads", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "noargs"},
	})
	if len(tools) != 2 || tools[0].Type != "function" || tools[0].Function.Name != "read_file" {
		t.Fatalf("tools = %+v", tools)
	}
	if string(tools[1].Function.Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("default params = %s", tools[1].Function.Parameters)
	}
}

// TestChatStreamAccumulator feeds delta chunks and checks reassembly of text,
// a tool call built across chunks, finish reason, and usage.
func TestChatStreamAccumulator(t *testing.T) {
	acc := &chatStreamAccumulator{}
	for _, chunk := range []string{
		`{"choices":[{"delta":{"role":"assistant","content":"work"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_7","type":"function","function":{"name":"run","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go build\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":9}}`,
	} {
		if err := acc.add([]byte(chunk)); err != nil {
			t.Fatalf("add(%s): %v", chunk, err)
		}
	}
	resp := acc.result()
	if resp.Text != "work" || resp.StopReason != provider.StopToolUse {
		t.Errorf("text/stop = %q / %v", resp.Text, resp.StopReason)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].ID != "call_7" || resp.ToolUses[0].Name != "run" {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	if string(resp.ToolUses[0].Input) != `{"cmd":"go build"}` {
		t.Errorf("assembled tool input = %s", resp.ToolUses[0].Input)
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 9 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestChatStreamAccumulatorError(t *testing.T) {
	err := (&chatStreamAccumulator{}).add([]byte(`{"error":{"message":"model not enabled"}}`))
	if err == nil || !strings.Contains(err.Error(), "model not enabled") {
		t.Errorf("expected chat error, got %v", err)
	}
}

func TestChatStopReasons(t *testing.T) {
	cases := map[string]provider.StopReason{
		"stop":       provider.StopEndTurn,
		"length":     provider.StopMaxTokens,
		"tool_calls": provider.StopToolUse,
		"other":      provider.StopEndTurn,
	}
	for fr, want := range cases {
		if got := chatStopReason(fr, false); got != want {
			t.Errorf("chatStopReason(%q) = %v, want %v", fr, got, want)
		}
	}
	if chatStopReason("stop", true) != provider.StopToolUse {
		t.Error("tool calls should force StopToolUse")
	}
}

// TestConverseChatStreamRoundTrip drives the chat_completions provider end to end
// against an SSE server, verifying the path, streaming request flags, and
// reassembly.
func TestConverseChatStreamRoundTrip(t *testing.T) {
	var gotPath string
	var gotReq chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotReq)
		writeSSE(w,
			`{"choices":[{"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			"[DONE]",
		)
	}))
	defer srv.Close()

	p, err := New(map[string]string{
		"mantle_endpoint": srv.URL,
		"bedrock_api_key": "k",
		"mantle_api":      "chat_completions",
		"model_id":        "qwen.qwen3-coder-next",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Converse(context.Background(), &provider.Request{
		System:   "sys",
		Messages: []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if !gotReq.Stream || gotReq.StreamOptions == nil || !gotReq.StreamOptions.IncludeUsage {
		t.Errorf("expected stream + include_usage, got %+v", gotReq)
	}
	if resp.Text != "done" || resp.StopReason != provider.StopEndTurn {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 1 || resp.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}
