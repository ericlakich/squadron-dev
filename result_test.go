package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ericlakich/squadron-dev/agent"
)

func fixedSession() *Session {
	ts := time.Date(2026, 7, 10, 2, 42, 33, 0, time.UTC)
	return &Session{
		ID:           "develop-20260710-024233-24d704",
		Phase:        "develop",
		Repo:         "ZipTax/ziptax-docs",
		Branch:       "ZIP-1188/plan-badges",
		BaseBranch:   "main",
		Status:       statusCompleted,
		PRURL:        "https://github.com/ZipTax/ziptax-docs/pull/41",
		FilesChanged: []string{"fern/docs/pages/sdks/overview.mdx"},
		CreatedAt:    ts,
		UpdatedAt:    ts,
	}
}

// TestPhaseResultJSONSchema pins the JSON schema for a fully-populated develop
// result: the exact set of keys and the derived/typed values.
func TestPhaseResultJSONSchema(t *testing.T) {
	sess := fixedSession()
	r := &agent.Result{
		FinalText:    "Added plan badges to the SDK overview page.",
		Iterations:   10,
		ToolCalls:    22,
		InputTokens:  93661,
		OutputTokens: 835,
		Transcript:   []agent.TranscriptEntry{{Turn: 1, Text: "Investigating"}, {Turn: 4, Text: "Editing"}},
	}
	out, err := newPhaseResult("LocalDev Development Complete", sess, r, nil, "Opened pull request: ...").renderJSON()
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	gotKeys := make([]string, 0, len(m))
	for k := range m {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{
		"base_branch", "branch", "created_at", "files_changed", "input_tokens",
		"iterations", "note", "output_tokens", "phase", "pr_number", "pr_url",
		"repo", "session_id", "status", "summary", "tool_calls", "transcript", "updated_at",
	}
	sort.Strings(wantKeys)
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("JSON keys mismatch\n got: %v\nwant: %v", gotKeys, wantKeys)
	}

	// Derived and typed values.
	if m["pr_number"].(float64) != 41 {
		t.Errorf("pr_number = %v, want 41 (derived from pr_url)", m["pr_number"])
	}
	if m["status"] != statusCompleted {
		t.Errorf("status = %v", m["status"])
	}
	if m["summary"] != "Added plan badges to the SDK overview page." {
		t.Errorf("summary = %v", m["summary"])
	}
	if m["created_at"] != "2026-07-10T02:42:33Z" {
		t.Errorf("created_at = %v", m["created_at"])
	}
	if len(m["transcript"].([]any)) != 2 {
		t.Errorf("transcript = %v, want 2 entries", m["transcript"])
	}
}

// TestPhaseResultJSONOmitsEmpty verifies optional fields drop out for a minimal
// (no-changes) result, keeping the schema tight.
func TestPhaseResultJSONOmitsEmpty(t *testing.T) {
	sess := &Session{
		ID:        "qa-1",
		Phase:     "qa",
		Status:    statusNoChanges,
		CreatedAt: time.Unix(0, 0).UTC(),
		UpdatedAt: time.Unix(0, 0).UTC(),
	}
	out, err := newPhaseResult("t", sess, &agent.Result{}, nil, "").renderJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	for _, absent := range []string{"pr_url", "pr_number", "branch", "files_changed", "transcript", "note", "error", "iterations"} {
		if _, ok := m[absent]; ok {
			t.Errorf("expected %q to be omitted when empty", absent)
		}
	}
	// Core fields are always present.
	for _, present := range []string{"session_id", "phase", "status", "summary", "created_at", "updated_at"} {
		if _, ok := m[present]; !ok {
			t.Errorf("expected core field %q to be present", present)
		}
	}
}

func TestRenderDispatchesByFormat(t *testing.T) {
	sess := fixedSession()
	r := &agent.Result{FinalText: "done"}

	jsonPlugin := &Plugin{cfg: &Settings{ResponseFormat: responseFormatJSON}}
	out, err := jsonPlugin.render("LocalDev Development Complete", sess, r, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("json format did not produce valid JSON:\n%s", out)
	}

	textPlugin := &Plugin{cfg: &Settings{ResponseFormat: responseFormatText}}
	out, err = textPlugin.render("LocalDev Development Complete", sess, r, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid([]byte(out)) || !strings.Contains(out, "=== LocalDev Development Complete ===") {
		t.Errorf("text format should be the human-readable summary, got:\n%s", out)
	}
}

func TestRenderStatusDispatchesByFormat(t *testing.T) {
	sess := fixedSession()
	jsonPlugin := &Plugin{cfg: &Settings{ResponseFormat: responseFormatJSON}}
	out, err := jsonPlugin.renderStatus(sess)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || m["session_id"] != sess.ID {
		t.Errorf("renderStatus json = %q (err %v)", out, err)
	}
}

func TestStripAlreadyCompleteMarker(t *testing.T) {
	r := &agent.Result{FinalText: "STATUS: already_completed\nThe plan badges already exist on this branch."}
	if !stripAlreadyCompleteMarker(r) {
		t.Fatal("expected marker to be detected")
	}
	if r.FinalText != "The plan badges already exist on this branch." {
		t.Errorf("marker not stripped cleanly: %q", r.FinalText)
	}
	// No marker -> untouched.
	r2 := &agent.Result{FinalText: "Implemented the change."}
	if stripAlreadyCompleteMarker(r2) || r2.FinalText != "Implemented the change." {
		t.Error("non-marker text should be left untouched")
	}
}

func TestPRNumberFromURL(t *testing.T) {
	if n := prNumberFromURL("https://github.com/ZipTax/ziptax-docs/pull/41"); n != 41 {
		t.Errorf("prNumberFromURL = %d, want 41", n)
	}
	if n := prNumberFromURL(""); n != 0 {
		t.Errorf("empty url = %d, want 0", n)
	}
	if n := prNumberFromURL("not a url"); n != 0 {
		t.Errorf("non-pr url = %d, want 0", n)
	}
}
