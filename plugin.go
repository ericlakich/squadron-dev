package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	squadron "github.com/mlund01/squadron-sdk"

	"github.com/ericlakich/squadron-dev/provider"
	"github.com/ericlakich/squadron-dev/vcs"
)

// tools defines the metadata for every tool the plugin exposes to Squadron. The
// three phases — code_develop, code_qa, code_review — are the primary surface;
// workspace_status and cleanup_workspace manage the local session directories.
var tools = map[string]*squadron.ToolInfo{
	"code_develop": {
		Name: "code_develop",
		Description: "Code Development phase. Clone a GitHub repository to the local filesystem, then drive a " +
			"provider-backed coding agent (AWS Bedrock) to implement the requested change: it explores the repo, " +
			"writes and edits files, runs builds and tests locally, and iterates until done. The plugin then commits " +
			"the work to a new branch, pushes it, and opens a pull request. Use this for features, bug fixes, or refactors.",
		Schema: squadron.Schema{
			Type: squadron.TypeObject,
			Properties: squadron.PropertyMap{
				"repo_url":     {Type: squadron.TypeString, Description: "Full URL of the GitHub repository (e.g. https://github.com/org/repo)"},
				"task":         {Type: squadron.TypeString, Description: "Description of the development task to perform (the direction for the agent)"},
				"branch":       {Type: squadron.TypeString, Description: "Optional name for the branch to create. If omitted, a name is generated from the task."},
				"base_branch":  {Type: squadron.TypeString, Description: "Optional base branch to branch from and target with the PR. Defaults to the repository's default branch."},
				"instructions": {Type: squadron.TypeString, Description: "Optional additional context, constraints, or coding guidelines"},
			},
			Required: []string{"repo_url", "task"},
		},
	},
	"code_qa": {
		Name: "code_qa",
		Description: "QA phase. Check out a pull request or branch into a local workspace and drive the agent to " +
			"build the project, run the test suite, and report bugs, edge cases, missing coverage, regressions, and " +
			"performance concerns. Read-only with respect to the source: the agent may run commands but does not modify, " +
			"commit, or push code. Returns a structured QA report.",
		Schema: squadron.Schema{
			Type: squadron.TypeObject,
			Properties: squadron.PropertyMap{
				"pr_url":       {Type: squadron.TypeString, Description: "Full URL of the GitHub pull request to QA (e.g. https://github.com/org/repo/pull/123). Provide this or repo_url."},
				"repo_url":     {Type: squadron.TypeString, Description: "Full URL of the GitHub repository, when QAing a branch rather than a PR"},
				"branch":       {Type: squadron.TypeString, Description: "Optional branch to check out when using repo_url. Defaults to the default branch."},
				"instructions": {Type: squadron.TypeString, Description: "Optional focus areas or additional instructions for the QA review"},
			},
		},
	},
	"code_review": {
		Name: "code_review",
		Description: "Review phase. Check out a pull request or branch into a local workspace, gather its diff, and " +
			"drive the agent to perform a read-only code review covering correctness, quality, security, conventions, and " +
			"test adequacy. Optionally posts the review back to the GitHub PR. Returns the review and a summary.",
		Schema: squadron.Schema{
			Type: squadron.TypeObject,
			Properties: squadron.PropertyMap{
				"pr_url":        {Type: squadron.TypeString, Description: "Full URL of the GitHub pull request to review. Provide this or repo_url."},
				"repo_url":      {Type: squadron.TypeString, Description: "Full URL of the GitHub repository, when reviewing a branch rather than a PR"},
				"branch":        {Type: squadron.TypeString, Description: "Optional branch to review when using repo_url"},
				"base_branch":   {Type: squadron.TypeString, Description: "Optional base branch to diff a branch against. Defaults to the repository's default branch."},
				"instructions":  {Type: squadron.TypeString, Description: "Optional focus areas or additional instructions for the review"},
				"post_comments": {Type: squadron.TypeBoolean, Description: "If true and reviewing a PR, post the review as a comment on the GitHub PR (requires GITHUB_TOKEN)"},
			},
		},
	},
	"workspace_status": {
		Name:        "workspace_status",
		Description: "Inspect a local development session created by a previous phase. Returns its status, repository, branch, pull request, token usage, and the agent's final summary.",
		Schema: squadron.Schema{
			Type: squadron.TypeObject,
			Properties: squadron.PropertyMap{
				"session_id": {Type: squadron.TypeString, Description: "The session id returned by code_develop, code_qa, or code_review"},
			},
			Required: []string{"session_id"},
		},
	},
	"cleanup_workspace": {
		Name:        "cleanup_workspace",
		Description: "Delete a local development session's workspace directory to reclaim disk space. Call this once a session's results are no longer needed.",
		Schema: squadron.Schema{
			Type: squadron.TypeObject,
			Properties: squadron.PropertyMap{
				"session_id": {Type: squadron.TypeString, Description: "The session id to delete"},
			},
			Required: []string{"session_id"},
		},
	},
}

// Plugin implements squadron.ToolProvider for local, provider-backed development.
type Plugin struct {
	cfg      *Settings
	provider provider.Provider
	github   *vcs.GitHub
}

// Configure parses settings, builds the configured provider, and prepares the
// GitHub client. See the README for the full settings reference. The Bedrock API
// key and GitHub token are read from settings (typically wired to Squadron
// secrets), each falling back to the environment when omitted.
func (p *Plugin) Configure(settings map[string]string) error {
	cfg, err := parseSettings(settings)
	if err != nil {
		return err
	}
	prov, err := provider.New(cfg.Provider, cfg.Raw)
	if err != nil {
		return err
	}
	p.cfg = cfg
	p.provider = prov
	p.github = vcs.NewGitHub(cfg.GitHubToken)
	return nil
}

// Call dispatches a tool invocation to the appropriate phase or session handler.
func (p *Plugin) Call(ctx context.Context, toolName string, payload string) (string, error) {
	if p.provider == nil {
		return "", fmt.Errorf("plugin not configured: call Configure first")
	}
	switch toolName {
	case "code_develop":
		var params developParams
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return "", fmt.Errorf("invalid payload: %w", err)
		}
		if params.RepoURL == "" || params.Task == "" {
			return "", fmt.Errorf("repo_url and task are required")
		}
		return p.runDevelop(ctx, params)
	case "code_qa":
		var params qaParams
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return "", fmt.Errorf("invalid payload: %w", err)
		}
		if params.PRURL == "" && params.RepoURL == "" {
			return "", fmt.Errorf("provide pr_url or repo_url")
		}
		return p.runQA(ctx, params)
	case "code_review":
		var params reviewParams
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return "", fmt.Errorf("invalid payload: %w", err)
		}
		if params.PRURL == "" && params.RepoURL == "" {
			return "", fmt.Errorf("provide pr_url or repo_url")
		}
		return p.runReview(ctx, params)
	case "workspace_status":
		var params sessionParams
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return "", fmt.Errorf("invalid payload: %w", err)
		}
		if params.SessionID == "" {
			return "", fmt.Errorf("session_id is required")
		}
		sess, err := loadSession(p.cfg.WorkspaceRoot, params.SessionID)
		if err != nil {
			return "", err
		}
		return p.renderStatus(sess)
	case "cleanup_workspace":
		var params sessionParams
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return "", fmt.Errorf("invalid payload: %w", err)
		}
		if params.SessionID == "" {
			return "", fmt.Errorf("session_id is required")
		}
		if err := os.RemoveAll(sessionDir(p.cfg.WorkspaceRoot, params.SessionID)); err != nil {
			return "", fmt.Errorf("remove session %s: %w", params.SessionID, err)
		}
		return fmt.Sprintf("Removed workspace for session %s.", params.SessionID), nil
	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

// GetToolInfo returns metadata for a specific tool.
func (p *Plugin) GetToolInfo(toolName string) (*squadron.ToolInfo, error) {
	info, ok := tools[toolName]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}
	return info, nil
}

// ListTools returns metadata for all tools provided by this plugin.
func (p *Plugin) ListTools() ([]*squadron.ToolInfo, error) {
	result := make([]*squadron.ToolInfo, 0, len(tools))
	for _, info := range tools {
		result = append(result, info)
	}
	return result, nil
}

// --- tool parameter types ---

type developParams struct {
	RepoURL      string `json:"repo_url"`
	Task         string `json:"task"`
	Branch       string `json:"branch,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

type qaParams struct {
	PRURL        string `json:"pr_url,omitempty"`
	RepoURL      string `json:"repo_url,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

type reviewParams struct {
	PRURL        string `json:"pr_url,omitempty"`
	RepoURL      string `json:"repo_url,omitempty"`
	Branch       string `json:"branch,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	PostComments bool   `json:"post_comments,omitempty"`
}

type sessionParams struct {
	SessionID string `json:"session_id"`
}
