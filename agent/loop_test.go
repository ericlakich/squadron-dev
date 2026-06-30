package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ericlakich/squadron-plugin-localdev/provider"
)

// fakeProvider returns a scripted sequence of responses, one per Converse call,
// and records the requests it received.
type fakeProvider struct {
	turns []provider.Response
	calls int
	last  *provider.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Converse(_ context.Context, req *provider.Request) (*provider.Response, error) {
	f.last = req
	r := f.turns[f.calls]
	f.calls++
	return &r, nil
}

func echoTool(calls *int) Tool {
	return Tool{
		Name:        "echo",
		Description: "echo",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		Handler: func(_ context.Context, in json.RawMessage) (string, error) {
			*calls++
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(in, &p)
			return "echo: " + p.Text, nil
		},
	}
}

func TestRunExecutesToolThenFinishes(t *testing.T) {
	fp := &fakeProvider{turns: []provider.Response{
		{
			StopReason: provider.StopToolUse,
			Text:       "let me call the tool",
			ToolUses:   []provider.ToolUse{{ID: "t1", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}},
			Usage:      provider.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{
			StopReason: provider.StopEndTurn,
			Text:       "all done",
			Usage:      provider.Usage{InputTokens: 8, OutputTokens: 3},
		},
	}}

	var toolCalls int
	res, err := Run(context.Background(), fp, []Tool{echoTool(&toolCalls)}, "do the thing", Options{MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.FinalText != "all done" {
		t.Errorf("FinalText = %q, want %q", res.FinalText, "all done")
	}
	if toolCalls != 1 {
		t.Errorf("tool invoked %d times, want 1", toolCalls)
	}
	if res.ToolCalls != 1 {
		t.Errorf("res.ToolCalls = %d, want 1", res.ToolCalls)
	}
	if res.Iterations != 2 {
		t.Errorf("res.Iterations = %d, want 2", res.Iterations)
	}
	if res.InputTokens != 18 || res.OutputTokens != 8 {
		t.Errorf("token totals = %d/%d, want 18/8", res.InputTokens, res.OutputTokens)
	}
	// The second request must carry the tool result back to the model.
	if fp.last == nil || len(fp.last.Messages) != 3 {
		t.Fatalf("expected 3 messages on final turn (user, assistant, tool-result), got %d", len(fp.last.Messages))
	}
	tr := fp.last.Messages[2].Blocks[0].ToolResult
	if tr == nil || tr.ToolUseID != "t1" || tr.Content != "echo: hi" {
		t.Errorf("tool result block = %+v, want id t1 / content 'echo: hi'", tr)
	}
}

func TestRunReportsUnknownToolWithoutCrashing(t *testing.T) {
	fp := &fakeProvider{turns: []provider.Response{
		{StopReason: provider.StopToolUse, ToolUses: []provider.ToolUse{{ID: "x", Name: "missing"}}},
		{StopReason: provider.StopEndTurn, Text: "recovered"},
	}}
	res, err := Run(context.Background(), fp, nil, "go", Options{MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.FinalText != "recovered" {
		t.Errorf("FinalText = %q, want 'recovered'", res.FinalText)
	}
	tr := fp.last.Messages[2].Blocks[0].ToolResult
	if tr == nil || !tr.IsError {
		t.Errorf("expected an error tool-result for the unknown tool, got %+v", tr)
	}
}

func TestRunStopsAtIterationLimit(t *testing.T) {
	// Always asks for a tool, never finishing.
	loop := provider.Response{StopReason: provider.StopToolUse, ToolUses: []provider.ToolUse{{ID: "t", Name: "echo", Input: json.RawMessage(`{"text":"x"}`)}}}
	fp := &fakeProvider{turns: []provider.Response{loop, loop, loop, loop}}
	var calls int
	res, err := Run(context.Background(), fp, []Tool{echoTool(&calls)}, "go", Options{MaxIterations: 3})
	if err == nil {
		t.Fatal("expected an error when the iteration limit is hit")
	}
	if !res.StoppedEarly {
		t.Error("expected StoppedEarly = true")
	}
	if res.Iterations != 3 {
		t.Errorf("res.Iterations = %d, want 3", res.Iterations)
	}
}
