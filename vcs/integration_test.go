package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupRemote builds a bare "origin" repo with a main branch (one commit) and a
// develop branch (an extra commit), and points origin/HEAD at main. It returns a
// file:// URL suitable for cloning.
func setupRemote(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	seed := filepath.Join(base, "seed")

	mustGit(t, "", "init", "--bare", "-b", "main", origin)
	mustGit(t, "", "init", "-b", "main", seed)
	mustGit(t, seed, "config", "user.name", "Test")
	mustGit(t, seed, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-m", "init")
	mustGit(t, seed, "remote", "add", "origin", origin)
	mustGit(t, seed, "push", "-u", "origin", "main")

	mustGit(t, seed, "checkout", "-b", "develop")
	if err := os.WriteFile(filepath.Join(seed, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-m", "develop work")
	mustGit(t, seed, "push", "-u", "origin", "develop")

	// Make main the default branch reported by origin/HEAD.
	mustGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	_ = ctx
	return "file://" + origin
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestCloneCheckoutBranchAndPush(t *testing.T) {
	url := setupRemote(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "clone")

	g, err := Clone(ctx, url, dir, "")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if db := g.DefaultBranch(ctx); db != "main" {
		t.Errorf("DefaultBranch = %q, want main", db)
	}

	// CheckoutRef on the branch already checked out (main) must not fail — this is
	// the regression the "fetch ref:ref into current branch" bug caused.
	if _, err := g.CheckoutRef(ctx, "main"); err != nil {
		t.Errorf("CheckoutRef(main) while on main failed: %v", err)
	}

	// Check out develop and confirm its extra file is present.
	if _, err := g.CheckoutRef(ctx, "develop"); err != nil {
		t.Fatalf("CheckoutRef(develop): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Errorf("expected b.txt on develop branch: %v", err)
	}

	// Branch off develop, commit, and push.
	if err := g.CreateBranch(ctx, "work"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, err := g.CommitAll(ctx, "add c")
	if err != nil || !committed {
		t.Fatalf("CommitAll committed=%v err=%v", committed, err)
	}
	if err := g.Push(ctx, "work"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// FetchRef + a tip diff against develop should show the new file.
	if err := g.FetchRef(ctx, "develop"); err != nil {
		t.Fatalf("FetchRef(develop): %v", err)
	}
	diff, err := g.Diff(ctx, "FETCH_HEAD..HEAD")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "c.txt") {
		t.Errorf("diff did not mention the new file c.txt:\n%s", diff)
	}
}

func TestCommitAllNoChanges(t *testing.T) {
	url := setupRemote(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "clone")
	g, err := Clone(ctx, url, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	committed, err := g.CommitAll(ctx, "noop")
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Error("CommitAll reported a commit with a clean tree")
	}
}
