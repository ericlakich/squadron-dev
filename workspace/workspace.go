// Package workspace provides a sandboxed view of a directory on the local
// filesystem in which a development session operates. Every file operation is
// constrained to the workspace root so that a model-directed agent cannot read
// or write outside the area it was given. The local filesystem is the workspace:
// repositories are cloned into it and all reads, writes, searches, and command
// execution happen relative to its root.
package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Workspace is a sandboxed directory on the local filesystem.
type Workspace struct {
	root     string // absolute, lexical root
	realRoot string // root with symlinks resolved, for containment checks
}

// Open ensures root exists and returns a Workspace rooted at its absolute path.
func Open(root string) (*Workspace, error) {
	abs, err := filepath.Abs(expandHome(root))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace %s: %w", abs, err)
	}
	// Resolve symlinks in the root itself (e.g. macOS /var -> /private/var) so the
	// containment check compares like with like.
	realRoot := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		realRoot = resolved
	}
	return &Workspace{root: abs, realRoot: realRoot}, nil
}

// Root returns the absolute path of the workspace root.
func (w *Workspace) Root() string { return w.root }

// resolve maps a workspace-relative path to an absolute path, rejecting any path
// that would escape the workspace root either lexically (via "..") or through a
// symlink whose real target lies outside the root.
func (w *Workspace) resolve(rel string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(rel))
	if clean == "" || clean == "." {
		return w.root, nil
	}
	var abs string
	if filepath.IsAbs(clean) {
		abs = clean
	} else {
		abs = filepath.Join(w.root, clean)
	}
	// Lexical containment.
	if abs != w.root && !strings.HasPrefix(abs, w.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	// Symlink-aware containment: resolve symlinks on the deepest existing prefix
	// (the final component may not exist yet for a write) and re-check.
	real := realPath(abs)
	if real != w.realRoot && !strings.HasPrefix(real, w.realRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the workspace via a symlink", rel)
	}
	return abs, nil
}

// realPath resolves symlinks on the deepest existing prefix of p and rejoins the
// remaining not-yet-existing suffix, so a path can be checked before it is created.
func realPath(p string) string {
	cur := p
	var tail []string
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if len(tail) == 0 {
				return resolved
			}
			for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
				tail[i], tail[j] = tail[j], tail[i]
			}
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // reached the filesystem root without resolving anything
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// firstComponent returns the first path element of a cleaned relative path.
func firstComponent(rel string) string {
	clean := filepath.Clean(strings.TrimSpace(rel))
	clean = strings.TrimPrefix(clean, string(os.PathSeparator))
	if first, _, found := strings.Cut(clean, string(os.PathSeparator)); found {
		return first
	}
	return clean
}

// Rel returns the workspace-relative form of an absolute path inside the root.
func (w *Workspace) Rel(abs string) string {
	r, err := filepath.Rel(w.root, abs)
	if err != nil {
		return abs
	}
	return r
}

// ReadFile returns the contents of a file in the workspace.
func (w *Workspace) ReadFile(rel string) (string, error) {
	abs, err := w.resolve(rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// WriteFile writes content to a file in the workspace, creating parent
// directories as needed. Writes into the .git directory are rejected so a
// model-directed agent cannot tamper with git internals or hooks.
func (w *Workspace) WriteFile(rel, content string) error {
	if firstComponent(rel) == ".git" {
		return fmt.Errorf("refusing to write inside the .git directory: %q", rel)
	}
	abs, err := w.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// ListDir lists the entries of a directory in the workspace. Directories are
// suffixed with "/".
func (w *Workspace) ListDir(rel string) ([]string, error) {
	abs, err := w.resolve(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// SearchResult is one matching line from a Search.
type SearchResult struct {
	Path string
	Line int
	Text string
}

// Search walks dir (relative to the root) and returns lines matching pattern.
// .git and common dependency directories are skipped, as are files that look
// binary. Results are capped at limit.
func (w *Workspace) Search(pattern, dir string, limit int) ([]SearchResult, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	base, err := w.resolve(dir)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}

	var results []SearchResult
	walkErr := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(results) >= limit {
			return filepath.SkipAll
		}
		b, err := os.ReadFile(path)
		if err != nil || looksBinary(b) {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) {
				results = append(results, SearchResult{
					Path: w.Rel(path),
					Line: i + 1,
					Text: strings.TrimSpace(line),
				})
				if len(results) >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return results, walkErr
	}
	return results, nil
}

// CommandResult captures the outcome of running a shell command.
type CommandResult struct {
	Stdout   string
	ExitCode int
	TimedOut bool
}

// RunCommand executes command with "sh -c" in the workspace root, capturing
// combined stdout/stderr. The command is bounded by ctx (the caller is
// responsible for any deadline). Output is capped at maxOutput bytes.
func (w *Workspace) RunCommand(ctx context.Context, command string, maxOutput int) (*CommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = w.root
	out, err := cmd.CombinedOutput()

	res := &CommandResult{Stdout: capBytes(out, maxOutput)}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil && !res.TimedOut {
		return res, err
	}
	return res, nil
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "venv", "__pycache__", "dist", "build", ".next", "target":
		return true
	}
	return false
}

func looksBinary(b []byte) bool {
	n := min(len(b), 8000)
	for i := range n {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

func capBytes(b []byte, max int) string {
	if max <= 0 {
		max = 60_000
	}
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + fmt.Sprintf("\n... [output truncated, %d of %d bytes shown]", max, len(b))
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
