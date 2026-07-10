package bedrock

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ericlakich/squadron-dev/provider"
)

func TestBearerOptionsConfiguresToken(t *testing.T) {
	opts := bearerOptions("sk-bedrock-secret")
	if len(opts) != 1 {
		t.Fatalf("expected 1 client option, got %d", len(opts))
	}
	var o bedrockruntime.Options
	opts[0](&o)
	if len(o.AuthSchemePreference) != 1 || o.AuthSchemePreference[0] != "httpBearerAuth" {
		t.Errorf("AuthSchemePreference = %v, want [httpBearerAuth]", o.AuthSchemePreference)
	}
	if o.BearerAuthTokenProvider == nil {
		t.Fatal("BearerAuthTokenProvider was not set")
	}
	tok, err := o.BearerAuthTokenProvider.RetrieveBearerToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "sk-bedrock-secret" {
		t.Errorf("bearer token = %q, want sk-bedrock-secret", tok.Value)
	}
}

func TestBearerOptionsEmptyWithoutKey(t *testing.T) {
	if opts := bearerOptions(""); opts != nil {
		t.Errorf("expected no options without a key, got %d", len(opts))
	}
}

func TestProviderRegistered(t *testing.T) {
	// Registered under the canonical name and the backward-compatible alias.
	for _, want := range []string{"bedrock-runtime", "bedrock"} {
		found := false
		for _, n := range provider.Available() {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("provider %q was not registered", want)
		}
	}
}

func TestNewParsesRequestTimeout(t *testing.T) {
	p, err := New(map[string]string{"request_timeout_seconds": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*Bedrock).requestTimeout; got != 42*time.Second {
		t.Errorf("requestTimeout = %s, want 42s", got)
	}
	// Unset falls back to the shared default.
	p2, err := New(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p2.(*Bedrock).requestTimeout; got != provider.DefaultRequestTimeout {
		t.Errorf("default requestTimeout = %s, want %s", got, provider.DefaultRequestTimeout)
	}
	if _, err := New(map[string]string{"request_timeout_seconds": "nope"}); err == nil {
		t.Error("expected error for invalid request_timeout_seconds")
	}
}

func TestDocumentRoundTrip(t *testing.T) {
	raw := json.RawMessage(`{"text":"hi","count":3}`)
	doc := document.NewLazyDocument(rawToAny(raw))
	got, err := documentToRaw(doc)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if m["text"] != "hi" {
		t.Errorf("text = %v, want hi", m["text"])
	}
	if m["count"].(float64) != 3 {
		t.Errorf("count = %v, want 3", m["count"])
	}
}

func TestToToolConfigParsesSchema(t *testing.T) {
	tc, err := toToolConfig([]provider.ToolSpec{{
		Name:        "read_file",
		Description: "reads",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tc.Tools))
	}
	spec, ok := tc.Tools[0].(*types.ToolMemberToolSpec)
	if !ok {
		t.Fatal("tool is not a ToolMemberToolSpec")
	}
	if *spec.Value.Name != "read_file" {
		t.Errorf("tool name = %q", *spec.Value.Name)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[types.StopReason]provider.StopReason{
		types.StopReasonEndTurn:             provider.StopEndTurn,
		types.StopReasonStopSequence:        provider.StopEndTurn,
		types.StopReasonToolUse:             provider.StopToolUse,
		types.StopReasonMaxTokens:           provider.StopMaxTokens,
		types.StopReasonGuardrailIntervened: provider.StopOther,
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestNewStreamMode(t *testing.T) {
	cases := []struct {
		mode     string
		wantErr  bool
	}{
		{"stream", false},
		{"non-streaming", false},
		{"STREAM", true},
		{"nonstreaming", true},
		{"", false}, // empty defaults to "stream"
	}
	for _, c := range cases {
		settings := map[string]string{
			"bedrock_api_key": "k",
		}
		if c.mode != "" {
			settings["stream_mode"] = c.mode
		}
		_, err := New(settings)
		if c.wantErr && err == nil {
			t.Errorf("stream_mode=%q should error but didn't", c.mode)
		}
		if !c.wantErr && err != nil {
			t.Errorf("stream_mode=%q should not error but got: %v", c.mode, err)
		}
	}
}

func TestNewDefaultStreamMode(t *testing.T) {
	p, err := New(map[string]string{"bedrock_api_key": "k"})
	if err != nil {
		t.Fatal(err)
	}
	b := p.(*Bedrock)
	if b.streamMode != "stream" {
		t.Errorf("default stream_mode = %q, want %q", b.streamMode, "stream")
	}
}
