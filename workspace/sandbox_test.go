package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSymlinkEscapeRejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	// File symlink inside the workspace pointing outside it.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReadFile("link"); err == nil {
		t.Error("expected read through a file symlink that escapes the root to be rejected")
	}

	// Directory symlink inside the workspace pointing outside it.
	if err := os.Symlink(outside, filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReadFile("d/secret.txt"); err == nil {
		t.Error("expected read through a directory symlink that escapes the root to be rejected")
	}
	if err := ws.WriteFile("d/pwned.txt", "x"); err == nil {
		t.Error("expected write through a directory symlink that escapes the root to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
		t.Error("write escaped the sandbox and created a file outside the root")
	}

	// A legitimate write inside the workspace must still succeed.
	if err := ws.WriteFile("pkg/ok.txt", "fine"); err != nil {
		t.Errorf("legitimate in-workspace write was rejected: %v", err)
	}
}

func TestGitWriteRejected(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git/hooks/pre-commit", ".git/config", ".git/x"} {
		if err := ws.WriteFile(p, "x"); err == nil {
			t.Errorf("expected write to %q to be rejected", p)
		}
	}
	// A file merely containing ".git" in the name elsewhere is fine.
	if err := ws.WriteFile("src/.gitignore", "node_modules\n"); err != nil {
		t.Errorf("write to src/.gitignore should be allowed: %v", err)
	}
}
