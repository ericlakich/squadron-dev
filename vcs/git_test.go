package vcs

import (
	"strings"
	"testing"
)

func TestRedactStripsCredentials(t *testing.T) {
	token := "ghp_SUPERSECRETTOKEN"
	in := "git fetch https://x-access-token:" + token + "@github.com/org/repo refs/heads/main: fatal: auth failed for https://x-access-token:" + token + "@github.com/org/repo"
	out := redact(in, token)
	if strings.Contains(out, token) {
		t.Errorf("token leaked through redact: %q", out)
	}
	if !strings.Contains(out, "***@github.com") {
		t.Errorf("expected userinfo to be redacted to ***@, got %q", out)
	}
}

func TestRedactLiteralToken(t *testing.T) {
	token := "abc123token"
	// Even if a token appears outside a URL, it must be scrubbed.
	out := redact("error: bad credential abc123token in helper", token)
	if strings.Contains(out, token) {
		t.Errorf("literal token not redacted: %q", out)
	}
}

func TestUsernameURLHasNoSecret(t *testing.T) {
	got := usernameURL("https://github.com/org/repo.git", "ghp_secret")
	want := "https://x-access-token@github.com/org/repo.git"
	if got != want {
		t.Errorf("usernameURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "ghp_secret") {
		t.Errorf("usernameURL leaked the token: %q", got)
	}
	if got := usernameURL("https://github.com/org/repo.git", ""); got != "https://github.com/org/repo.git" {
		t.Errorf("usernameURL with no token = %q", got)
	}
}
