package main

import (
	"strings"
	"testing"

	"github.com/ericlakich/squadron-dev/agent"
)

func TestFormatPhaseIncludesAgentActivity(t *testing.T) {
	sess := &Session{ID: "develop-1", Status: "completed"}
	r := &agent.Result{
		FinalText: "Opened the PR.",
		Transcript: []agent.TranscriptEntry{
			{Turn: 1, Text: "Investigating the failing test."},
			{Turn: 3, Text: "Patching handler.go\nand adding a case."},
		},
	}
	out := formatPhase("LocalDev Development Complete", sess, r, nil, "")

	if !strings.Contains(out, "--- Agent Activity ---") {
		t.Fatalf("missing Agent Activity section:\n%s", out)
	}
	if !strings.Contains(out, "- turn 1: Investigating the failing test.") {
		t.Errorf("missing turn 1 line:\n%s", out)
	}
	// Multi-line turn text is collapsed to a single line.
	if !strings.Contains(out, "- turn 3: Patching handler.go and adding a case.") {
		t.Errorf("turn 3 not collapsed to one line:\n%s", out)
	}
	// The final summary is still present and distinct from the activity log.
	if !strings.Contains(out, "--- Agent Summary ---") || !strings.Contains(out, "Opened the PR.") {
		t.Errorf("missing final summary:\n%s", out)
	}
}

func TestFormatPhaseNoActivityWhenEmpty(t *testing.T) {
	sess := &Session{ID: "qa-1", Status: "completed"}
	out := formatPhase("LocalDev QA Review", sess, &agent.Result{}, nil, "")
	if strings.Contains(out, "--- Agent Activity ---") {
		t.Errorf("should omit Agent Activity when transcript is empty:\n%s", out)
	}
}

func TestWriteAgentActivityIsBounded(t *testing.T) {
	// Build a transcript far larger than the byte budget.
	long := strings.Repeat("x", maxActivityLine*2)
	var transcript []agent.TranscriptEntry
	for i := 1; i <= 200; i++ {
		transcript = append(transcript, agent.TranscriptEntry{Turn: i, Text: long})
	}
	var b strings.Builder
	writeAgentActivity(&b, transcript)
	out := b.String()

	if len(out) > maxActivityChars+400 {
		t.Errorf("activity section not bounded: %d bytes", len(out))
	}
	// Each rendered line is truncated to the per-line cap (plus the ellipsis rune).
	for line := range strings.SplitSeq(out, "\n") {
		if len([]rune(line)) > maxActivityLine+40 {
			t.Errorf("line exceeds per-line cap: %q", line)
		}
	}
	if !strings.Contains(out, "200 turns total") {
		t.Errorf("expected a total-turns note when truncated:\n%s", out)
	}
}

func TestFormatStatusIncludesAgentActivity(t *testing.T) {
	sess := &Session{
		ID:     "develop-2",
		Phase:  "develop",
		Status: "completed",
		Transcript: []agent.TranscriptEntry{
			{Turn: 2, Text: "Ran go test ./... — all pass."},
		},
	}
	out := formatStatus(sess)
	if !strings.Contains(out, "--- Agent Activity ---") || !strings.Contains(out, "- turn 2: Ran go test") {
		t.Errorf("workspace_status missing agent activity:\n%s", out)
	}
}
