// Package agent implements the local agentic loop that powers each development
// phase. Given a provider (the LLM brain), a set of local tools, a system prompt,
// and a directive, Run drives a tool-use conversation: it asks the model what to
// do, executes any tools the model requests against the local workspace, feeds
// the results back, and repeats until the model stops requesting tools or a
// budget is exhausted.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ericlakich/squadron-dev/provider"
)

// Options configures a single agent run.
type Options struct {
	System        string
	MaxIterations int
	MaxTokens     int
	Temperature   float32
	// RepoContext, if set, is appended to the system prompt as authoritative
	// repository guidance (the repo's own instruction/convention files).
	RepoContext string
	// Logf, if set, receives progress messages (one per turn / tool call).
	Logf func(format string, args ...any)
}

// Result summarizes an agent run.
type Result struct {
	FinalText    string
	Iterations   int
	ToolCalls    int
	InputTokens  int
	OutputTokens int
	// StoppedEarly is true if the run hit MaxIterations before the model finished.
	StoppedEarly bool
	// Truncated is true if the model's final turn was cut off by the token limit,
	// so FinalText (and any in-progress work) may be incomplete.
	Truncated bool
}

// Run executes the agent loop. It returns a Result describing the run; the
// model's final assistant text is the phase summary.
func Run(ctx context.Context, p provider.Provider, tools []Tool, directive string, opts Options) (*Result, error) {
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = 50
	}

	byName := make(map[string]Tool, len(tools))
	specs := make([]provider.ToolSpec, 0, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		specs = append(specs, t.Spec())
	}

	// Fold the repository's own guidance into the system prompt so it is
	// authoritative for every turn.
	system := opts.System
	if strings.TrimSpace(opts.RepoContext) != "" {
		system += "\n\n# Repository guidance\n\n" +
			"The following are instruction and convention files found in this repository. " +
			"Treat them as authoritative guidance for how to work in this codebase " +
			"(build/test commands, style, and project rules):\n\n" + opts.RepoContext
	}

	messages := []provider.Message{
		{Role: provider.RoleUser, Blocks: []provider.Block{provider.TextBlock(directive)}},
	}

	res := &Result{}
	for i := 0; i < opts.MaxIterations; i++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.Iterations = i + 1

		resp, err := p.Converse(ctx, &provider.Request{
			System:      system,
			Messages:    messages,
			Tools:       specs,
			MaxTokens:   opts.MaxTokens,
			Temperature: opts.Temperature,
		})
		if err != nil {
			return res, err
		}
		res.InputTokens += resp.Usage.InputTokens
		res.OutputTokens += resp.Usage.OutputTokens

		// Record the assistant turn (text + any tool-use requests) so the next
		// request carries the full conversation, as Bedrock requires.
		asstBlocks := make([]provider.Block, 0, 1+len(resp.ToolUses))
		if strings.TrimSpace(resp.Text) != "" {
			asstBlocks = append(asstBlocks, provider.TextBlock(resp.Text))
			res.FinalText = resp.Text
			if opts.Logf != nil {
				opts.Logf("turn %d: %s", res.Iterations, oneLine(resp.Text, 200))
			}
		}
		for _, tu := range resp.ToolUses {
			asstBlocks = append(asstBlocks, provider.Block{ToolUse: &tu})
		}
		messages = append(messages, provider.Message{Role: provider.RoleAssistant, Blocks: asstBlocks})

		// Terminal: the model produced a normal answer with no tool calls (or was
		// cut off by the token limit, which we flag so callers don't present a
		// truncated result as finished).
		if resp.StopReason != provider.StopToolUse || len(resp.ToolUses) == 0 {
			if resp.StopReason == provider.StopMaxTokens {
				res.Truncated = true
			}
			return res, nil
		}

		// Execute every requested tool and return the results in a single user
		// message, one tool-result block per request (Bedrock requires a result
		// for each tool_use id in the turn).
		resultBlocks := make([]provider.Block, 0, len(resp.ToolUses))
		for _, tu := range resp.ToolUses {
			res.ToolCalls++
			content, isErr := dispatch(ctx, byName, tu)
			if opts.Logf != nil {
				status := "ok"
				if isErr {
					status = "error"
				}
				opts.Logf("  tool %s (%s): %s", tu.Name, status, oneLine(content, 120))
			}
			resultBlocks = append(resultBlocks, provider.Block{
				ToolResult: &provider.ToolResult{ToolUseID: tu.ID, Content: content, IsError: isErr},
			})
		}
		messages = append(messages, provider.Message{Role: provider.RoleUser, Blocks: resultBlocks})
	}

	res.StoppedEarly = true
	return res, fmt.Errorf("agent did not finish within %d iterations", opts.MaxIterations)
}

// dispatch runs a single tool, returning its output (or error text) and whether
// it failed.
func dispatch(ctx context.Context, byName map[string]Tool, tu provider.ToolUse) (string, bool) {
	tool, ok := byName[tu.Name]
	if !ok {
		return fmt.Sprintf("unknown tool: %s", tu.Name), true
	}
	out, err := tool.Handler(ctx, tu.Input)
	if err != nil {
		return "error: " + err.Error(), true
	}
	if strings.TrimSpace(out) == "" {
		return "(no output)", false
	}
	return out, false
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
