package main

import (
	"strings"
	"testing"

	"github.com/ericlakich/squadron-plugin-localdev/workspace"
)

func TestGatherRepoContext(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.WriteFile("AGENTS.md", "Run `go test ./...` before committing.")
	_ = ws.WriteFile("README.md", "# Project")
	_ = ws.WriteFile(".cursor/rules/style.mdc", "Use tabs, not spaces.")

	got := gatherRepoContext(ws, &Settings{LoadRepoContext: true, MaxContextBytes: 10000})

	for _, want := range []string{"AGENTS.md", "Run `go test", "README.md", "style.mdc", "tabs"} {
		if !strings.Contains(got, want) {
			t.Errorf("context missing %q\n---\n%s", want, got)
		}
	}
	// Priority ordering: AGENTS.md must precede README.md.
	if strings.Index(got, "AGENTS.md") > strings.Index(got, "README.md") {
		t.Error("expected AGENTS.md to be loaded before README.md")
	}
}

func TestGatherRepoContextDisabled(t *testing.T) {
	ws, _ := workspace.Open(t.TempDir())
	_ = ws.WriteFile("AGENTS.md", "x")
	if got := gatherRepoContext(ws, &Settings{LoadRepoContext: false, MaxContextBytes: 1000}); got != "" {
		t.Errorf("expected empty context when disabled, got %q", got)
	}
}

func TestGatherRepoContextRespectsBudget(t *testing.T) {
	ws, _ := workspace.Open(t.TempDir())
	_ = ws.WriteFile("AGENTS.md", strings.Repeat("a", 5000))
	got := gatherRepoContext(ws, &Settings{LoadRepoContext: true, MaxContextBytes: 200})
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected a truncation marker when over budget:\n%s", got)
	}
	if len(got) > 1000 {
		t.Errorf("context not bounded by budget: %d bytes", len(got))
	}
}
