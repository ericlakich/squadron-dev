package vcs

import "testing"

func TestParseRepoURL(t *testing.T) {
	cases := map[string]Repo{
		"https://github.com/org/repo":      {Owner: "org", Name: "repo"},
		"https://github.com/org/repo.git":  {Owner: "org", Name: "repo"},
		"https://github.com/org/repo/":     {Owner: "org", Name: "repo"},
		"git@github.com:org/repo.git":      {Owner: "org", Name: "repo"},
		"https://github.com/Org-1/my.repo": {Owner: "Org-1", Name: "my.repo"},
	}
	for in, want := range cases {
		got, err := ParseRepoURL(in)
		if err != nil {
			t.Errorf("ParseRepoURL(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseRepoURL(%q) = %+v, want %+v", in, got, want)
		}
	}
	if _, err := ParseRepoURL("not a url"); err == nil {
		t.Error("expected error for invalid repo URL")
	}
}

func TestParsePRURL(t *testing.T) {
	repo, n, err := ParsePRURL("https://github.com/org/repo/pull/123")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Owner != "org" || repo.Name != "repo" || n != 123 {
		t.Errorf("ParsePRURL = %+v #%d, want org/repo #123", repo, n)
	}
	if _, _, err := ParsePRURL("https://github.com/org/repo"); err == nil {
		t.Error("expected error for a non-PR URL")
	}
}

func TestAuthenticatedURL(t *testing.T) {
	got := authenticatedURL("https://github.com/org/repo.git", "secret")
	want := "https://x-access-token:secret@github.com/org/repo.git"
	if got != want {
		t.Errorf("authenticatedURL = %q, want %q", got, want)
	}
	// No token => unchanged.
	if got := authenticatedURL("https://github.com/org/repo.git", ""); got != "https://github.com/org/repo.git" {
		t.Errorf("authenticatedURL with no token = %q", got)
	}
}
