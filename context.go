package main

import (
	"fmt"
	"strings"

	"github.com/ericlakich/squadron-plugin-localdev/workspace"
)

// defaultContextFiles are the repository instruction/convention files the agent
// loads as context, in priority order. These cover the common conventions used by
// AI coding agents (AGENTS.md, CLAUDE.md, Cursor, Copilot, Windsurf, Gemini) plus
// the key human-facing project docs. Higher-priority files are loaded first and a
// large low-priority file (e.g. README) is what gets truncated when the budget is
// reached.
var defaultContextFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"GEMINI.md",
	".cursorrules",
	".windsurfrules",
	".github/copilot-instructions.md",
	".goosehints",
	"CONTRIBUTING.md",
	"README.md",
}

// gatherRepoContext reads the repository's own instruction/convention files from
// the local checkout and concatenates them into a single guidance block, bounded
// by cfg.MaxContextBytes. It returns an empty string when context loading is
// disabled or nothing is found. The returned block is injected into the agent's
// system prompt so the agent follows the repo's documented conventions and
// build/test commands — a key part of operating autonomously on a real codebase.
func gatherRepoContext(ws *workspace.Workspace, cfg *Settings) string {
	if !cfg.LoadRepoContext || cfg.MaxContextBytes <= 0 {
		return ""
	}

	files := cfg.ContextFiles
	if len(files) == 0 {
		files = defaultContextFiles
	}

	var b strings.Builder
	total := 0
	seen := map[string]bool{}

	add := func(path string) {
		if seen[path] || total >= cfg.MaxContextBytes {
			return
		}
		seen[path] = true
		content, err := ws.ReadFile(path)
		if err != nil || strings.TrimSpace(content) == "" {
			return
		}
		remaining := cfg.MaxContextBytes - total
		truncated := false
		if len(content) > remaining {
			content = content[:remaining]
			truncated = true
		}
		fmt.Fprintf(&b, "===== %s =====\n%s\n", path, content)
		if truncated {
			b.WriteString("... [truncated]\n")
		}
		b.WriteString("\n")
		total += len(content)
	}

	for _, f := range files {
		add(f)
	}

	// Cursor's newer rules format: .cursor/rules/*.mdc (and *.md).
	if entries, err := ws.ListDir(".cursor/rules"); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e, ".mdc") || strings.HasSuffix(e, ".md") {
				add(".cursor/rules/" + e)
			}
		}
	}

	return strings.TrimSpace(b.String())
}
