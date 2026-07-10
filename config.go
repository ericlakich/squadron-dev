package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultProvider        = "bedrock-mantle"
	defaultMaxIterations   = 50
	defaultCommandTimeout  = 5 * time.Minute
	defaultMaxOutputBytes  = 60_000
	defaultMaxContextBytes = 32_768
	defaultGitUserName     = "Squadron LocalDev"
	defaultGitUserEmail    = "localdev@squadron.sh"

	// response_format values.
	responseFormatText = "text"
	responseFormatJSON = "json"
)

// Settings is the parsed plugin configuration. Secret credentials — the Bedrock
// API key and the GitHub token — may be supplied through the settings block
// (typically wired to Squadron secrets), falling back to the environment. The
// Bedrock API key is forwarded to the provider via Raw; the GitHub token is
// resolved here into GitHubToken.
type Settings struct {
	Provider       string
	ResponseFormat string
	WorkspaceRoot  string
	MaxIterations  int
	CommandTimeout time.Duration
	MaxOutputBytes int
	GitUserName    string
	GitUserEmail   string
	AutoPush       bool
	OpenPR         bool
	BaseBranch     string
	GitHubToken    string
	// CloneDepth is the git clone depth. 0 (the default) means a full clone so the
	// agent has the complete codebase and history; set a positive value for a
	// faster shallow clone of very large repositories.
	CloneDepth int
	// LoadRepoContext controls whether the repository's own instruction/convention
	// files are loaded into the agent's context.
	LoadRepoContext bool
	// ContextFiles overrides the default set of instruction files to load (relative
	// paths). Empty means use defaultContextFiles.
	ContextFiles []string
	// MaxContextBytes caps the total size of loaded repo-context files.
	MaxContextBytes int
	// Raw holds the full settings map, forwarded to the provider factory.
	Raw map[string]string
}

func parseSettings(s map[string]string) (*Settings, error) {
	if s == nil {
		s = map[string]string{}
	}
	cfg := &Settings{
		Provider:        getOr(s, "provider", defaultProvider),
		ResponseFormat:  getOr(s, "response_format", responseFormatText),
		WorkspaceRoot:   getOr(s, "workspace_root", defaultWorkspaceRoot()),
		MaxIterations:   defaultMaxIterations,
		CommandTimeout:  defaultCommandTimeout,
		MaxOutputBytes:  defaultMaxOutputBytes,
		GitUserName:     getOr(s, "git_user_name", defaultGitUserName),
		GitUserEmail:    getOr(s, "git_user_email", defaultGitUserEmail),
		AutoPush:        getBool(s, "auto_push", true),
		OpenPR:          getBool(s, "open_pr", true),
		BaseBranch:      s["base_branch"],
		GitHubToken:     getOr(s, "github_token", githubTokenFromEnv()),
		LoadRepoContext: getBool(s, "load_repo_context", true),
		MaxContextBytes: defaultMaxContextBytes,
		Raw:             s,
	}

	if cfg.ResponseFormat != responseFormatText && cfg.ResponseFormat != responseFormatJSON {
		return nil, fmt.Errorf("invalid response_format %q: must be %q or %q", cfg.ResponseFormat, responseFormatText, responseFormatJSON)
	}

	if v := s["clone_depth"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid clone_depth %q: must be a non-negative integer (0 = full clone)", v)
		}
		cfg.CloneDepth = n
	}
	if v := s["max_context_bytes"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid max_context_bytes %q: must be a non-negative integer", v)
		}
		cfg.MaxContextBytes = n
	}
	if v := s["context_files"]; v != "" {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.ContextFiles = append(cfg.ContextFiles, p)
			}
		}
	}

	if v := s["max_iterations"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid max_iterations %q: must be a positive integer", v)
		}
		cfg.MaxIterations = n
	}
	if v := s["command_timeout_seconds"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid command_timeout_seconds %q: must be a positive integer", v)
		}
		cfg.CommandTimeout = time.Duration(n) * time.Second
	}
	if v := s["max_output_bytes"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1024 {
			return nil, fmt.Errorf("invalid max_output_bytes %q: must be at least 1024", v)
		}
		cfg.MaxOutputBytes = n
	}
	return cfg, nil
}

// githubTokenFromEnv reads a GitHub token from the environment. It is the
// fallback when the github_token setting (a Squadron secret) is not provided.
func githubTokenFromEnv() string {
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func defaultWorkspaceRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".squadron", "localdev", "workspaces")
	}
	return filepath.Join(os.TempDir(), "squadron-localdev", "workspaces")
}

func getOr(m map[string]string, key, def string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return def
}

func getBool(m map[string]string, key string, def bool) bool {
	v, ok := m[key]
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
