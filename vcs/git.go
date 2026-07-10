// Package vcs provides the version-control integration used by the plugin: local
// git operations (clone, branch, commit, push, diff) over the os/exec git binary,
// and a small GitHub REST client (see github.go) for opening pull requests and
// posting reviews.
//
// Authentication credentials are never stored in plugin configuration and are
// kept out of error output and persisted state:
//
//   - On Unix, network operations authenticate via a transient GIT_ASKPASS
//     helper that reads the token from the git child process's environment, so
//     the token never appears in the process argument list or in git config.
//   - On other platforms, the token is injected into the remote URL for the
//     single network call as a fallback. In all cases credentials are redacted
//     from any error message via redact().
package vcs

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Git runs git commands inside a single working directory.
type Git struct {
	dir   string
	token string
	// depth is the shallow-fetch depth for network operations; 0 means full.
	depth int
}

// NewGit returns a Git bound to dir. token, if non-empty, authenticates network
// operations.
func NewGit(dir, token string) *Git {
	return &Git{dir: dir, token: token}
}

// depthArgs returns the --depth flag for network operations, or nil for a full
// (unbounded) fetch/clone.
func (g *Git) depthArgs() []string {
	if g.depth > 0 {
		return []string{"--depth", strconv.Itoa(g.depth)}
	}
	return nil
}

// Dir returns the working directory.
func (g *Git) Dir() string { return g.dir }

// run executes a local (non-network) git subcommand in g.dir.
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	return runGitEnv(ctx, g.dir, g.token, nil, args...)
}

// runNetwork executes a network git subcommand (clone/fetch/push) with credentials
// supplied out-of-band on Unix, falling back to caller-provided args otherwise.
func (g *Git) runNetwork(ctx context.Context, args ...string) (string, error) {
	env, cleanup, err := askpassEnv(g.token)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return runGitEnv(ctx, g.dir, g.token, env, args...)
}

func runGitEnv(ctx context.Context, dir, token string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		out := redact(strings.TrimSpace(buf.String()), token)
		return out, fmt.Errorf("git %s: %w: %s", redact(strings.Join(args, " "), token), err, out)
	}
	return strings.TrimSpace(buf.String()), nil
}

// Clone clones repoURL into dir, authenticating without persisting a credential
// into the cloned repository's git config. depth bounds the history fetched
// (0 = full clone, giving the agent the complete codebase and history).
func Clone(ctx context.Context, repoURL, dir, token string, depth int) (*Git, error) {
	g := NewGit(dir, token)
	g.depth = depth
	cloneURL := networkURL(repoURL, token)
	// The clone runs with no working directory (dir does not exist yet); only
	// subsequent operations run inside the clone.
	env, cleanup, err := askpassEnv(token)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	args := append([]string{"clone"}, g.depthArgs()...)
	args = append(args, cloneURL, dir)
	if _, err := runGitEnv(ctx, "", token, env, args...); err != nil {
		return nil, err
	}
	// Normalize origin so no secret is persisted: on Unix the username-only URL
	// is safe (the password comes from askpass); elsewhere reset to the clean URL.
	if askpassSupported() {
		_, _ = g.run(ctx, "remote", "set-url", "origin", usernameURL(repoURL, token))
	} else {
		_, _ = g.run(ctx, "remote", "set-url", "origin", repoURL)
	}
	return g, nil
}

// ConfigureIdentity sets the commit author identity for this repository.
func (g *Git) ConfigureIdentity(ctx context.Context, name, email string) error {
	if name != "" {
		if _, err := g.run(ctx, "config", "user.name", name); err != nil {
			return err
		}
	}
	if email != "" {
		if _, err := g.run(ctx, "config", "user.email", email); err != nil {
			return err
		}
	}
	return nil
}

// CreateBranch creates and checks out a new branch from the current HEAD.
func (g *Git) CreateBranch(ctx context.Context, name string) error {
	_, err := g.run(ctx, "checkout", "-b", name)
	return err
}

// CheckoutPR fetches a GitHub pull request head into a local branch and checks
// it out. The remote ref is the standard refs/pull/<n>/head.
func (g *Git) CheckoutPR(ctx context.Context, number int) (string, error) {
	branch := fmt.Sprintf("pr-%d", number)
	args := append([]string{"fetch"}, g.depthArgs()...)
	args = append(args, g.remote(ctx), fmt.Sprintf("pull/%d/head:%s", number, branch))
	if _, err := g.runNetwork(ctx, args...); err != nil {
		return "", err
	}
	if _, err := g.run(ctx, "checkout", branch); err != nil {
		return "", err
	}
	return branch, nil
}

// CheckoutRef fetches an existing branch from origin and checks it out. It fetches
// into FETCH_HEAD and uses "checkout -B" so it works even when ref is already the
// checked-out branch (git refuses a direct fetch into the current branch).
func (g *Git) CheckoutRef(ctx context.Context, ref string) (string, error) {
	args := append([]string{"fetch"}, g.depthArgs()...)
	args = append(args, g.remote(ctx), ref)
	if _, err := g.runNetwork(ctx, args...); err != nil {
		return "", err
	}
	if _, err := g.run(ctx, "checkout", "-B", ref, "FETCH_HEAD"); err != nil {
		return "", err
	}
	return ref, nil
}

// FetchRef best-effort fetches a ref from origin so it is available locally for
// diffing. Errors are returned for the caller to handle or ignore.
func (g *Git) FetchRef(ctx context.Context, ref string) error {
	args := append([]string{"fetch"}, g.depthArgs()...)
	args = append(args, g.remote(ctx), ref)
	_, err := g.runNetwork(ctx, args...)
	return err
}

// CurrentBranch returns the name of the checked-out branch.
func (g *Git) CurrentBranch(ctx context.Context) (string, error) {
	return g.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
}

// HasChanges reports whether the working tree has uncommitted changes.
func (g *Git) HasChanges(ctx context.Context) (bool, error) {
	out, err := g.run(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// CommitAll stages all changes and commits them. It returns false if there was
// nothing to commit.
func (g *Git) CommitAll(ctx context.Context, message string) (bool, error) {
	changed, err := g.HasChanges(ctx)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if _, err := g.run(ctx, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := g.run(ctx, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// Push pushes branch to origin.
func (g *Git) Push(ctx context.Context, branch string) error {
	_, err := g.runNetwork(ctx, "push", "-u", g.remote(ctx), branch)
	return err
}

// Diff returns the diff produced by "git diff <spec>".
func (g *Git) Diff(ctx context.Context, spec string) (string, error) {
	return g.run(ctx, "diff", spec)
}

// ChangedFilesInHEAD returns the paths modified by the HEAD commit (including a
// root commit). It is best-effort: callers that only want the list for reporting
// should tolerate an error by ignoring the result.
func (g *Git) ChangedFilesInHEAD(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", "HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// DefaultBranch returns the repository's default branch as reported by origin/HEAD,
// falling back to "main".
func (g *Git) DefaultBranch(ctx context.Context) string {
	out, err := g.run(ctx, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err == nil {
		if _, b, ok := strings.Cut(out, "/"); ok && b != "" {
			return b
		}
	}
	return "main"
}

// remote returns the remote argument for network operations: the plain "origin"
// remote when using askpass (Unix) or no token, and an explicitly authenticated
// URL on the fallback path.
func (g *Git) remote(ctx context.Context) string {
	if g.token == "" || askpassSupported() {
		return "origin"
	}
	out, err := g.run(ctx, "remote", "get-url", "origin")
	if err != nil || out == "" {
		return "origin"
	}
	return authenticatedURL(out, g.token)
}

// networkURL returns the URL to clone from: username-only on Unix (askpass
// supplies the password) and token-embedded on the fallback path.
func networkURL(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	if askpassSupported() {
		return usernameURL(repoURL, token)
	}
	return authenticatedURL(repoURL, token)
}

// askpassSupported reports whether the GIT_ASKPASS shell-helper approach works on
// this platform.
func askpassSupported() bool { return runtime.GOOS != "windows" }

// askpassEnv writes a transient GIT_ASKPASS helper that prints the token from the
// child process environment, returning the env additions and a cleanup func. The
// token is passed via the environment (readable only by the process owner), never
// on the command line. Returns no additions when there is no token or on an
// unsupported platform.
func askpassEnv(token string) (env []string, cleanup func(), err error) {
	noop := func() {}
	if token == "" || !askpassSupported() {
		return nil, noop, nil
	}
	dir, err := os.MkdirTemp("", "localdev-askpass-")
	if err != nil {
		return nil, noop, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	script := filepath.Join(dir, "askpass.sh")
	// git calls the helper with the prompt as $1; we ignore it and always print
	// the token (the username is supplied in the remote URL).
	body := "#!/bin/sh\nprintf '%s' \"$LOCALDEV_GIT_TOKEN\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		cleanup()
		return nil, noop, err
	}
	return []string{
		"GIT_ASKPASS=" + script,
		"LOCALDEV_GIT_TOKEN=" + token,
		"GIT_TERMINAL_PROMPT=0",
	}, cleanup, nil
}

// usernameURL embeds only the username (not the token) into an https GitHub URL.
func usernameURL(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" {
		return repoURL
	}
	u.User = url.User("x-access-token")
	return u.String()
}

// authenticatedURL injects an access token into an https GitHub URL. Non-https
// URLs are returned unchanged.
func authenticatedURL(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" {
		return repoURL
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

var urlCredRe = regexp.MustCompile(`(https?://)[^/@\s]*:[^/@\s]*@`)

// redact removes credentials from a string before it is surfaced in an error or
// persisted: it strips userinfo from any URL and replaces literal occurrences of
// the token.
func redact(s, token string) string {
	if s == "" {
		return s
	}
	s = urlCredRe.ReplaceAllString(s, "$1***@")
	if token != "" {
		s = strings.ReplaceAll(s, token, "***")
	}
	return s
}
