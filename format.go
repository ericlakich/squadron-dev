package main

import (
	"fmt"
	"strings"

	"github.com/ericlakich/squadron-dev/agent"
)

// formatDevelop renders the result of the Code Development phase.
func formatDevelop(sess *Session, r *agent.Result, runErr error, note string) string {
	return formatPhase("LocalDev Development Complete", sess, r, runErr, note)
}

// formatPhase renders a phase result into a readable text summary that the
// Squadron agent consumes as the tool result.
func formatPhase(title string, sess *Session, r *agent.Result, runErr error, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s ===\n\n", title)
	fmt.Fprintf(&b, "Session: %s\n", sess.ID)
	if sess.Repo != "" {
		fmt.Fprintf(&b, "Repository: %s\n", sess.Repo)
	}
	if sess.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", sess.Branch)
	}
	if sess.BaseBranch != "" {
		fmt.Fprintf(&b, "Base: %s\n", sess.BaseBranch)
	}
	if sess.PRURL != "" {
		fmt.Fprintf(&b, "Pull Request: %s\n", sess.PRURL)
	}
	fmt.Fprintf(&b, "Status: %s\n", sess.Status)
	if r != nil {
		fmt.Fprintf(&b, "Agent activity: %d turns, %d tool calls, %d in / %d out tokens",
			r.Iterations, r.ToolCalls, r.InputTokens, r.OutputTokens)
		if r.StoppedEarly {
			b.WriteString(" (stopped at iteration limit)")
		}
		b.WriteString("\n")
		if r.Truncated {
			b.WriteString("WARNING: the model's final response was truncated by the token limit; the summary below may be incomplete. Consider raising max_tokens.\n")
		}
	}
	if note != "" {
		fmt.Fprintf(&b, "\n%s\n", note)
	}
	if runErr != nil {
		fmt.Fprintf(&b, "\nNote: the agent loop ended with an error: %s\n", runErr.Error())
	}

	if r != nil {
		writeAgentActivity(&b, r.Transcript)
	}

	b.WriteString("\n--- Agent Summary ---\n\n")
	if r != nil && strings.TrimSpace(r.FinalText) != "" {
		b.WriteString(r.FinalText)
		b.WriteString("\n")
	} else {
		b.WriteString("The agent did not produce a final summary.\n")
	}

	fmt.Fprintf(&b, "\nInspect this session later with workspace_status (session_id: %s).\n", sess.ID)
	return b.String()
}

const (
	// maxActivityChars bounds the Agent Activity section so a long run doesn't
	// flood the tool result (and the orchestrator's context). Full detail always
	// remains in the plugin logs.
	maxActivityChars = 4000
	maxActivityLine  = 240
)

// writeAgentActivity renders the agent's turn-by-turn responses as a bounded
// bullet list. Each turn is condensed to a single line; rendering stops once the
// size budget is reached, noting how many turns were captured in total.
func writeAgentActivity(b *strings.Builder, transcript []agent.TranscriptEntry) {
	if len(transcript) == 0 {
		return
	}
	b.WriteString("\n--- Agent Activity ---\n\n")
	used := 0
	for i, e := range transcript {
		line := fmt.Sprintf("- turn %d: %s\n", e.Turn, oneLineText(e.Text, maxActivityLine))
		if used+len(line) > maxActivityChars && i > 0 {
			fmt.Fprintf(b, "- … (%d turns total; see plugin logs for full detail)\n", len(transcript))
			return
		}
		b.WriteString(line)
		used += len(line)
	}
}

// oneLineText collapses whitespace and truncates to a single readable line.
func oneLineText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// formatStatus renders a stored session for the workspace_status tool.
func formatStatus(sess *Session) string {
	var b strings.Builder
	b.WriteString("=== LocalDev Session Status ===\n\n")
	fmt.Fprintf(&b, "Session: %s\n", sess.ID)
	fmt.Fprintf(&b, "Phase: %s\n", sess.Phase)
	fmt.Fprintf(&b, "Status: %s\n", sess.Status)
	if sess.Repo != "" {
		fmt.Fprintf(&b, "Repository: %s\n", sess.Repo)
	}
	if sess.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", sess.Branch)
	}
	if sess.BaseBranch != "" {
		fmt.Fprintf(&b, "Base: %s\n", sess.BaseBranch)
	}
	if sess.PRURL != "" {
		fmt.Fprintf(&b, "Pull Request: %s\n", sess.PRURL)
	}
	fmt.Fprintf(&b, "Workspace: %s\n", sess.WorkspaceDir)
	fmt.Fprintf(&b, "Created: %s\n", sess.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "Updated: %s\n", sess.UpdatedAt.Format("2006-01-02 15:04:05 MST"))
	if sess.Iterations > 0 {
		fmt.Fprintf(&b, "Agent activity: %d turns, %d tool calls, %d in / %d out tokens\n",
			sess.Iterations, sess.ToolCalls, sess.InputTokens, sess.OutputTokens)
	}
	if sess.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", sess.Error)
	}
	writeAgentActivity(&b, sess.Transcript)
	if strings.TrimSpace(sess.Summary) != "" {
		b.WriteString("\n--- Agent Summary ---\n\n")
		b.WriteString(sess.Summary)
		b.WriteString("\n")
	}
	return b.String()
}
