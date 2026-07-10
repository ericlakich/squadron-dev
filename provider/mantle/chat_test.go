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
	msgsOut := toChatMessages("be helpful", msgs)
	b, _ := json.Marshal(msgsOut)
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	// system, user, assistant(+tool_calls), tool
	if len(items) != 4 {
		t.Fatalf("got %d messages, want 4: %s", len(items), b)
	}
	if items[0]["role"] != "system" || items[0]["content"] != "be helpful" {
		t.Errorf("system msg = %v", items[0])
	}
	if items[1]["role"] != "user" || items[1]["content"] != "do the thing" {
		t.Errorf("user msg = %v", items[1])
	}
	// assistant message carries content + tool_calls (nested function form)
	asst := items[2]
	if asst["role"] != "assistant" || asst["content"] != "on it" {
		t.Errorf("assistant msg = %v", asst)
	}
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_1" || tc["type"] != "function" {
		t.Errorf("tool_call = %v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "read_file" || fn["arguments"] != `{"path":"a.go"}` {
		t.Errorf("tool_call function = %v", fn)
	}
	// tool result -> role:tool message keyed by tool_call_id, empty -> placeholder
	tool := items[3]
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "(no output)" {
		t.Errorf("tool msg = %v", tool)
	}
}

func TestToChatMessagesNoSystem(t *testing.T) {
	out := toChatMessages("", []provider.Message{
		{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock("hi")}},
	})
	if len(out) != 1 || out[0].Role != "user" {
		t.Errorf("expected a single user message with no system, got %+v", out)
	}
}

func TestToChatToolsNestedForm(t *testing.T) {
	tools := toChatTools([]provider.ToolSpec{
		{Name: "read_file", Description: "reads", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "noargs"},
	})
	if len(tools) != 2 {
		t.Fatalf("got %d tools", len(tools))
	}
	if tools[0].Type != "function" || tools[0].Function.Name != "read_file" {
		t.Errorf("tool0 = %+v", tools[0])
	}
	if string(tools[1].Function.Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("default params = %s", tools[1].Function.Parameters)
	}
}

func TestParseChatTextAndToolCall(t *testing.T) {
	body := []byte(`{
		"choices":[{
			"message":{"role":"assistant","content":"working","tool_calls":[
				{"id":"call_7","type":"function","function":{"name":"run","arguments":"{\"cmd\":\"go build\"}"}}
			]},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":5,"completion_tokens":9}
	}`)
	resp, err := parseChatResponse("qwen.qwen3-coder-next", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "working" || resp.StopReason != provider.StopToolUse {
		t.Errorf("text/stop = %q / %v", resp.Text, resp.StopReason)
	}
	if len(resp.ToolUses) != 1 || resp.ToolUses[0].ID != "call_7" || resp.ToolUses[0].Name != "run" {
		t.Fatalf("tool uses = %+v", resp.ToolUses)
	}
	if string(resp.ToolUses[0].Input) != `{"cmd":"go build"}` {
		t.Errorf("tool input = %s", resp.ToolUses[0].Input)
	}
	if resp.Usage.InputTokens != 5 || resp.Usage.OutputTokens != 9 {
		t.Errorf("usage = %+v", resp.Usage)
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
	if got := chatStopReason("stop", true); got != provider.StopToolUse {
		t.Errorf("tool calls should force StopToolUse, got %v", got)
	}
}

func TestParseChatError(t *testing.T) {
	if _, err := parseChatResponse("m", []byte(`{"error":{"message":"model not enabled"}}`)); err == nil || !strings.Contains(err.Error(), "model not enabled") {
		t.Errorf("expected chat error, got %v", err)
	}
}

// TestConverseChatRoundTrip drives the chat_completions provider end to end,
// verifying the path, request mapping, and response mapping.
func TestConverseChatRoundTrip(t *testing.T) {
	var gotPath string
	var gotReq chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
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
		Tools:    []provider.ToolSpec{{Name: "t", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotReq.Model != "qwen.qwen3-coder-next" {
		t.Errorf("model = %q", gotReq.Model)
	}
	// system prompt becomes the first message, not a separate field.
	if len(gotReq.Messages) != 2 || gotReq.Messages[0].Role != "system" || gotReq.Messages[1].Role != "user" {
		t.Errorf("messages = %+v", gotReq.Messages)
	}
	if len(gotReq.Tools) != 1 || gotReq.Tools[0].Type != "function" {
		t.Errorf("tools = %+v", gotReq.Tools)
	}
	if resp.Text != "done" || resp.StopReason != provider.StopEndTurn {
		t.Errorf("resp = %+v", resp)
	}
}
