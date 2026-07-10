package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/ericlakich/squadron-dev/agent"
	"github.com/ericlakich/squadron-dev/vcs"
)

// Phase status values. "already_completed" is distinct from "no_changes" so a
// caller can tell "the requested work was already present" from "the agent
// produced nothing."
const (
	statusRunning          = "running"
	statusCompleted        = "completed"
	statusNoChanges        = "no_changes"
	statusAlreadyCompleted = "already_completed"
	statusFailed           = "failed"
)

// alreadyCompleteMarker is the sentinel the develop agent emits (as the first line
// of its final message) when the requested change is already present. See
// developSystem in prompts.go.
const alreadyCompleteMarker = "STATUS: already_completed"

// PhaseResult is the typed, source-of-truth result of a phase run. It is rendered
// either as free-form text (the default, unchanged behavior) or as JSON when
// response_format = "json", letting an orchestrator map fields directly into task
// outputs without parsing prose.
type PhaseResult struct {
	SessionID    string    `json:"session_id"`
	Phase        string    `json:"phase"`
	Repo         string    `json:"repo,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	BaseBranch   string    `json:"base_branch,omitempty"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary"`
	Transcript   []string  `json:"transcript,omitempty"`
	PRURL        string    `json:"pr_url,omitempty"`
	PRNumber     int       `json:"pr_number,omitempty"`
	FilesChanged []string  `json:"files_changed,omitempty"`
	Iterations   int       `json:"iterations,omitempty"`
	ToolCalls    int       `json:"tool_calls,omitempty"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	Truncated    bool      `json:"truncated,omitempty"`
	StoppedEarly bool      `json:"stopped_early,omitempty"`
	Note         string    `json:"note,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// title is used only by the text renderer and is not serialized.
	title string
}

// newPhaseResult builds a PhaseResult from a completed phase run.
func newPhaseResult(title string, sess *Session, r *agent.Result, runErr error, note string) *PhaseResult {
	pr := &PhaseResult{
		title:        title,
		SessionID:    sess.ID,
		Phase:        sess.Phase,
		Repo:         sess.Repo,
		Branch:       sess.Branch,
		BaseBranch:   sess.BaseBranch,
		Status:       sess.Status,
		PRURL:        sess.PRURL,
		FilesChanged: sess.FilesChanged,
		Note:         note,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
	}
	if runErr != nil {
		pr.Error = runErr.Error()
	} else {
		pr.Error = sess.Error
	}
	pr.PRNumber = prNumberFromURL(sess.PRURL)
	if r != nil {
		pr.Summary = r.FinalText
		pr.Iterations = r.Iterations
		pr.ToolCalls = r.ToolCalls
		pr.InputTokens = r.InputTokens
		pr.OutputTokens = r.OutputTokens
		pr.Truncated = r.Truncated
		pr.StoppedEarly = r.StoppedEarly
		pr.Transcript = transcriptStrings(r.Transcript)
	}
	return pr
}

// phaseResultFromSession builds a PhaseResult from a persisted session, for the
// workspace_status tool.
func phaseResultFromSession(sess *Session) *PhaseResult {
	return &PhaseResult{
		title:        "LocalDev Session Status",
		SessionID:    sess.ID,
		Phase:        sess.Phase,
		Repo:         sess.Repo,
		Branch:       sess.Branch,
		BaseBranch:   sess.BaseBranch,
		Status:       sess.Status,
		Summary:      sess.Summary,
		Transcript:   transcriptStrings(sess.Transcript),
		PRURL:        sess.PRURL,
		PRNumber:     prNumberFromURL(sess.PRURL),
		FilesChanged: sess.FilesChanged,
		Iterations:   sess.Iterations,
		ToolCalls:    sess.ToolCalls,
		InputTokens:  sess.InputTokens,
		OutputTokens: sess.OutputTokens,
		Error:        sess.Error,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
	}
}

// renderJSON serializes the result as indented JSON.
func (pr *PhaseResult) renderJSON() (string, error) {
	b, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// render returns the phase result in the plugin's configured response format.
func (p *Plugin) render(title string, sess *Session, r *agent.Result, runErr error, note string) (string, error) {
	if p.cfg.ResponseFormat == responseFormatJSON {
		return newPhaseResult(title, sess, r, runErr, note).renderJSON()
	}
	return formatPhase(title, sess, r, runErr, note), nil
}

// renderStatus returns a stored session in the plugin's configured response format.
func (p *Plugin) renderStatus(sess *Session) (string, error) {
	if p.cfg.ResponseFormat == responseFormatJSON {
		return phaseResultFromSession(sess).renderJSON()
	}
	return formatStatus(sess), nil
}

// transcriptStrings condenses a bounded prefix of the transcript to one line per
// turn, mirroring the text renderer's Agent Activity section.
func transcriptStrings(entries []agent.TranscriptEntry) []string {
	kept, _ := boundedTranscript(entries)
	if len(kept) == 0 {
		return nil
	}
	out := make([]string, 0, len(kept))
	for _, e := range kept {
		out = append(out, oneLineText(e.Text, maxActivityLine))
	}
	return out
}

// stripAlreadyCompleteMarker reports whether the agent signaled that the requested
// change was already present, removing the sentinel line from the summary so the
// relayed text stays clean.
func stripAlreadyCompleteMarker(r *agent.Result) bool {
	if r == nil {
		return false
	}
	text := strings.TrimSpace(r.FinalText)
	if !strings.HasPrefix(text, alreadyCompleteMarker) {
		return false
	}
	r.FinalText = strings.TrimSpace(strings.TrimPrefix(text, alreadyCompleteMarker))
	return true
}

// prNumberFromURL extracts the PR number from a GitHub PR URL, returning 0 when it
// is not a PR URL.
func prNumberFromURL(prURL string) int {
	if prURL == "" {
		return 0
	}
	if _, n, err := vcs.ParsePRURL(prURL); err == nil {
		return n
	}
	return 0
}
