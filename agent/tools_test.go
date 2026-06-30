package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ericlakich/squadron-dev/workspace"
)

// TestWorkspaceToolSchemasAreValidJSON guards the hand-written InputSchema
// strings in tools.go: the Go compiler does not validate JSON inside string
// literals, so a typo would only surface at runtime when sent to the provider.
func TestWorkspaceToolSchemasAreValidJSON(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tools := WorkspaceTools(ws, ToolOptions{AllowWrite: true, AllowCommands: true, CommandTimeout: time.Minute})
	if len(tools) == 0 {
		t.Fatal("no tools constructed")
	}
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Name == "" {
			t.Error("tool with empty name")
		}
		if seen[tl.Name] {
			t.Errorf("duplicate tool name %q", tl.Name)
		}
		seen[tl.Name] = true

		var obj map[string]any
		if err := json.Unmarshal(tl.InputSchema, &obj); err != nil {
			t.Errorf("tool %q has invalid InputSchema JSON: %v", tl.Name, err)
			continue
		}
		if obj["type"] != "object" {
			t.Errorf("tool %q schema type = %v, want object", tl.Name, obj["type"])
		}
	}

	// Read-only option set must exclude mutation and command tools.
	ro := WorkspaceTools(ws, ToolOptions{})
	for _, tl := range ro {
		switch tl.Name {
		case "write_file", "edit_file", "run_command":
			t.Errorf("read-only tool set unexpectedly includes %q", tl.Name)
		}
	}
}
