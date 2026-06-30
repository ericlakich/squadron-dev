package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	defaultProvider       = "bedrock"
	defaultMaxIterations  = 50
	defaultCommandTimeout = 5 * time.Minute
	defaultMaxOutputBytes = 60_000
	defaultGitUserName    = "Squadron LocalDev"
	defaultGitUserEmail   = "localdev@squadron.sh"
)

// Settings is the parsed plugin configuration. Provider credentials are NOT part
// of this struct: the AWS credential chain supplies Bedrock credentials, and the
// GitHub token is read from the environment (GITHUB_TOKEN / GH_TOKEN).
type Settings struct {
	Provider       string
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
	// Raw holds the full settings map, forwarded to the provider factory.
	Raw map[string]string
}

func parseSettings(s map[string]string) (*Settings, error) {
	if s == nil {
		s = map[string]string{}
	}
	cfg := &Settings{
		Provider:       getOr(s, "provider", defaultProvider),
		WorkspaceRoot:  getOr(s, "workspace_root", defaultWorkspaceRoot()),
		MaxIterations:  defaultMaxIterations,
		CommandTimeout: defaultCommandTimeout,
		MaxOutputBytes: defaultMaxOutputBytes,
		GitUserName:    getOr(s, "git_user_name", defaultGitUserName),
		GitUserEmail:   getOr(s, "git_user_email", defaultGitUserEmail),
		AutoPush:       getBool(s, "auto_push", true),
		OpenPR:         getBool(s, "open_pr", true),
		BaseBranch:     s["base_branch"],
		GitHubToken:    githubTokenFromEnv(),
		Raw:            s,
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

// githubTokenFromEnv reads a GitHub token from the environment only. Tokens are
// never accepted through plugin settings.
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
