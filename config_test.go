package main

import "testing"

func TestGitHubTokenFromSettings(t *testing.T) {
	cfg, err := parseSettings(map[string]string{"github_token": "gh-from-settings"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubToken != "gh-from-settings" {
		t.Errorf("GitHubToken = %q, want gh-from-settings", cfg.GitHubToken)
	}
}

func TestGitHubTokenFallsBackToEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "gh-from-env")
	cfg, err := parseSettings(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubToken != "gh-from-env" {
		t.Errorf("GitHubToken = %q, want gh-from-env (env fallback)", cfg.GitHubToken)
	}
}

func TestSettingsGitHubTokenBeatsEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "gh-from-env")
	cfg, err := parseSettings(map[string]string{"github_token": "gh-from-settings"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubToken != "gh-from-settings" {
		t.Errorf("GitHubToken = %q, want the settings value to win", cfg.GitHubToken)
	}
}

func TestResponseFormatDefaultAndValidation(t *testing.T) {
	cfg, err := parseSettings(map[string]string{})
	if err != nil || cfg.ResponseFormat != responseFormatText {
		t.Errorf("default ResponseFormat = %q, %v; want text", cfg.ResponseFormat, err)
	}
	cfg, err = parseSettings(map[string]string{"response_format": "json"})
	if err != nil || cfg.ResponseFormat != responseFormatJSON {
		t.Errorf("ResponseFormat = %q, %v; want json", cfg.ResponseFormat, err)
	}
	if _, err := parseSettings(map[string]string{"response_format": "yaml"}); err == nil {
		t.Error("expected error for invalid response_format")
	}
}

// The Bedrock API key is not resolved into Settings; it is forwarded verbatim to
// the provider factory via Raw.
func TestBedrockAPIKeyForwardedViaRaw(t *testing.T) {
	cfg, err := parseSettings(map[string]string{"bedrock_api_key": "sk-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Raw["bedrock_api_key"] != "sk-secret" {
		t.Errorf("Raw[bedrock_api_key] = %q, want sk-secret", cfg.Raw["bedrock_api_key"])
	}
}
