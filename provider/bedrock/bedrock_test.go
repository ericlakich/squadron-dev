package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ericlakich/squadron-plugin-localdev/provider"
)

func TestProviderRegistered(t *testing.T) {
	found := false
	for _, n := range provider.Available() {
		if n == "bedrock" {
			found = true
		}
	}
	if !found {
		t.Fatal("bedrock provider was not registered")
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
