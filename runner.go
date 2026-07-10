package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ericlakich/squadron-dev/agent"
	"github.com/ericlakich/squadron-dev/vcs"
	"github.com/ericlakich/squadron-dev/workspace"
)

// maxDiffBytes caps the diff embedded into a review directive so a very large PR
// does not blow the context window; the agent reads the rest via tools.
const maxDiffBytes = 120_000

// runDevelop runs the Code Development phase: clone the repo, branch off the base,
// let the local agent implement and verify the change, then commit, push, and
// open a pull request.
func (p *Plugin) runDevelop(ctx context.Context, params developParams) (string, error) {
	repo, err := vcs.ParseRepoURL(params.RepoURL)
	if err != nil {
		return "", err
	}

	sess := newSession(p.cfg.WorkspaceRoot, "develop")
	sess.Repo = repo.String()
	_ = sess.save(p.cfg.WorkspaceRoot)

	git, err := vcs.Clone(ctx, repo.CloneURL(), sess.WorkspaceDir, p.cfg.GitHubToken, p.cfg.CloneDepth)
	if err != nil {
		return p.fail(sess, fmt.Errorf("clone %s: %w", repo, err))
	}
	if err := git.ConfigureIdentity(ctx, p.cfg.GitUserName, p.cfg.GitUserEmail); err != nil {
		return p.fail(sess, err)
	}

	defaultBranch := git.DefaultBranch(ctx)
	base := firstNonEmpty(params.BaseBranch, p.cfg.BaseBranch, defaultBranch)
	branch := firstNonEmpty(params.Branch, generatedBranch(params.Task))
	sess.Branch = branch
	sess.BaseBranch = base
	// The shallow clone lands on the repository's default branch. If a different
	// base was requested, check it out first so the new branch is cut from the
	// correct base and the resulting PR diff is accurate.
	if base != defaultBranch {
		if _, err := git.CheckoutRef(ctx, base); err != nil {
			return p.fail(sess, fmt.Errorf("checkout base branch %s: %w", base, err))
		}
	}
	if err := git.CreateBranch(ctx, branch); err != nil {
		return p.fail(sess, fmt.Errorf("create branch %s: %w", branch, err))
	}

	ws, err := workspace.Open(sess.WorkspaceDir)
	if err != nil {
		return p.fail(sess, err)
	}
	repoCtx := gatherRepoContext(ws, p.cfg)
	tools := agent.WorkspaceTools(ws, agent.ToolOptions{
		AllowWrite:     true,
		AllowCommands:  true,
		CommandTimeout: p.cfg.CommandTimeout,
		MaxOutputBytes: p.cfg.MaxOutputBytes,
	})

	result, runErr := agent.Run(ctx, p.provider, tools,
		buildDevelopDirective(params.Task, branch, params.Instructions),
		agent.Options{System: developSystem, RepoContext: repoCtx, MaxIterations: p.cfg.MaxIterations, Logf: logf})
	applyResult(sess, result)
	if runErr != nil {
		// Persist whatever progress was made; surface the error in the summary.
		sess.Error = runErr.Error()
	}

	committed, err := git.CommitAll(ctx, commitMessage(params.Task, result))
	if err != nil {
		return p.fail(sess, err)
	}
	if !committed {
		sess.Status = "no_changes"
		_ = sess.save(p.cfg.WorkspaceRoot)
		return formatDevelop(sess, result, runErr, "The agent made no file changes."), nil
	}

	var note string
	if p.cfg.AutoPush {
		if p.cfg.GitHubToken == "" {
			note = "Changes committed locally. Skipped push/PR: no GITHUB_TOKEN in the environment."
		} else if err := git.Push(ctx, branch); err != nil {
			note = "Changes committed locally, but push failed: " + err.Error()
		} else if p.cfg.OpenPR {
			pr, prErr := p.github.CreatePR(ctx, repo, vcs.CreatePRInput{
				Title: prTitle(params.Task),
				Head:  branch,
				Base:  base,
				Body:  prBody(params.Task, params.Instructions, result),
			})
			if prErr != nil {
				note = "Pushed branch " + branch + ", but opening the PR failed: " + prErr.Error()
			} else {
				sess.PRURL = pr.HTMLURL
				note = "Opened pull request: " + pr.HTMLURL
			}
		} else {
			note = "Pushed branch " + branch + " (open_pr disabled)."
		}
	} else {
		note = "Changes committed locally on branch " + branch + " (auto_push disabled)."
	}

	sess.Status = "completed"
	_ = sess.save(p.cfg.WorkspaceRoot)
	return formatDevelop(sess, result, runErr, note), nil
}

// runQA runs the QA phase: check out the change (a PR or a branch) into a local
// workspace and let the agent build, test, and report. Nothing is written back.
func (p *Plugin) runQA(ctx context.Context, params qaParams) (string, error) {
	sess := newSession(p.cfg.WorkspaceRoot, "qa")
	_ = sess.save(p.cfg.WorkspaceRoot)

	target, _, err := p.checkout(ctx, sess, params.RepoURL, params.PRURL, params.Branch)
	if err != nil {
		return p.fail(sess, err)
	}

	ws, err := workspace.Open(sess.WorkspaceDir)
	if err != nil {
		return p.fail(sess, err)
	}
	repoCtx := gatherRepoContext(ws, p.cfg)
	tools := agent.WorkspaceTools(ws, agent.ToolOptions{
		AllowWrite:     false,
		AllowCommands:  true,
		CommandTimeout: p.cfg.CommandTimeout,
		MaxOutputBytes: p.cfg.MaxOutputBytes,
	})

	result, runErr := agent.Run(ctx, p.provider, tools,
		buildQADirective(target, params.Instructions),
		agent.Options{System: qaSystem, RepoContext: repoCtx, MaxIterations: p.cfg.MaxIterations, Logf: logf})
	applyResult(sess, result)
	if runErr != nil {
		sess.Error = runErr.Error()
	}
	sess.Status = "completed"
	_ = sess.save(p.cfg.WorkspaceRoot)
	return formatPhase("LocalDev QA Review", sess, result, runErr, ""), nil
}

// runReview runs the Review phase: check out the change, gather its diff, and let
// the agent review it read-only. Optionally posts the review to the PR.
func (p *Plugin) runReview(ctx context.Context, params reviewParams) (string, error) {
	sess := newSession(p.cfg.WorkspaceRoot, "review")
	_ = sess.save(p.cfg.WorkspaceRoot)

	target, repoNum, err := p.checkout(ctx, sess, params.RepoURL, params.PRURL, params.Branch)
	if err != nil {
		return p.fail(sess, err)
	}

	diff, diffNote := p.gatherDiff(ctx, sess, params, repoNum)

	ws, err := workspace.Open(sess.WorkspaceDir)
	if err != nil {
		return p.fail(sess, err)
	}
	repoCtx := gatherRepoContext(ws, p.cfg)
	tools := agent.WorkspaceTools(ws, agent.ToolOptions{
		AllowWrite:     false,
		AllowCommands:  false,
		MaxOutputBytes: p.cfg.MaxOutputBytes,
	})

	result, runErr := agent.Run(ctx, p.provider, tools,
		buildReviewDirective(target, params.Instructions, diff, maxDiffBytes),
		agent.Options{System: reviewSystem, RepoContext: repoCtx, MaxIterations: p.cfg.MaxIterations, Logf: logf})
	applyResult(sess, result)
	if runErr != nil {
		sess.Error = runErr.Error()
	}

	notes := []string{}
	if diffNote != "" {
		notes = append(notes, diffNote)
	}
	if params.PostComments && params.PRURL != "" && result != nil && strings.TrimSpace(result.FinalText) != "" {
		if p.cfg.GitHubToken == "" {
			notes = append(notes, "Skipped posting the review: no GITHUB_TOKEN in the environment.")
		} else if repo, n, perr := vcs.ParsePRURL(params.PRURL); perr == nil {
			if err := p.github.CreateReview(ctx, repo, n, result.FinalText, "COMMENT"); err != nil {
				notes = append(notes, "Failed to post the review to the PR: "+err.Error())
			} else {
				sess.PRURL = params.PRURL
				notes = append(notes, "Posted the review as a comment on "+params.PRURL)
			}
		}
	}

	sess.Status = "completed"
	_ = sess.save(p.cfg.WorkspaceRoot)
	return formatPhase("LocalDev Code Review", sess, result, runErr, strings.Join(notes, "\n")), nil
}

// checkout clones the target repo into the session workspace and checks out the
// PR or branch under review. It returns a human-readable target description and
// the PR number (0 if not a PR).
func (p *Plugin) checkout(ctx context.Context, sess *Session, repoURL, prURL, branch string) (string, int, error) {
	var repo vcs.Repo
	var prNum int
	var err error

	switch {
	case prURL != "":
		repo, prNum, err = vcs.ParsePRURL(prURL)
	case repoURL != "":
		repo, err = vcs.ParseRepoURL(repoURL)
	default:
		return "", 0, fmt.Errorf("provide either pr_url or repo_url")
	}
	if err != nil {
		return "", 0, err
	}
	sess.Repo = repo.String()

	git, err := vcs.Clone(ctx, repo.CloneURL(), sess.WorkspaceDir, p.cfg.GitHubToken, p.cfg.CloneDepth)
	if err != nil {
		return "", 0, fmt.Errorf("clone %s: %w", repo, err)
	}

	switch {
	case prNum > 0:
		b, err := git.CheckoutPR(ctx, prNum)
		if err != nil {
			return "", 0, fmt.Errorf("checkout PR #%d: %w", prNum, err)
		}
		sess.Branch = b
		return prURL, prNum, nil
	case branch != "":
		if _, err := git.CheckoutRef(ctx, branch); err != nil {
			return "", 0, fmt.Errorf("checkout branch %s: %w", branch, err)
		}
		sess.Branch = branch
		return fmt.Sprintf("%s @ %s", repo, branch), 0, nil
	default:
		sess.Branch = git.DefaultBranch(ctx)
		return fmt.Sprintf("%s @ %s", repo, sess.Branch), 0, nil
	}
}

// gatherDiff returns the diff for the change under review plus an optional note
// explaining any degradation. It prefers the GitHub API for PRs, then falls back
// to a local diff against the base branch. The base ref is fetched explicitly
// (the shallow clone may lack it) and compared via a merge-base (three-dot) diff,
// falling back to a tip-to-tip (two-dot) diff when the shallow history has no
// reachable merge-base. A failure is surfaced as a note rather than silently
// returning an empty diff that would masquerade as "no changes".
func (p *Plugin) gatherDiff(ctx context.Context, sess *Session, params reviewParams, prNum int) (string, string) {
	if prNum > 0 {
		if repo, n, err := vcs.ParsePRURL(params.PRURL); err == nil {
			if diff, err := p.github.GetPRDiff(ctx, repo, n); err == nil && strings.TrimSpace(diff) != "" {
				return diff, ""
			}
		}
	}

	git := vcs.NewGit(sess.WorkspaceDir, p.cfg.GitHubToken)
	base := firstNonEmpty(params.BaseBranch, p.cfg.BaseBranch, git.DefaultBranch(ctx))

	// Fetch the base ref so it is present locally as FETCH_HEAD.
	if err := git.FetchRef(ctx, base); err == nil {
		if diff, err := git.Diff(ctx, "FETCH_HEAD...HEAD"); err == nil && strings.TrimSpace(diff) != "" {
			return diff, ""
		}
		if diff, err := git.Diff(ctx, "FETCH_HEAD..HEAD"); err == nil {
			if strings.TrimSpace(diff) == "" {
				return "", "No diff found against base " + base + "; the review proceeded from the checked-out files."
			}
			return diff, "Note: no merge-base was reachable in the shallow clone, so this diff compares the base tip to HEAD directly and may include unrelated changes."
		}
	}

	// Last resort: the default remote-tracking branch from the original clone.
	if diff, err := git.Diff(ctx, "origin/"+git.DefaultBranch(ctx)+"...HEAD"); err == nil && strings.TrimSpace(diff) != "" {
		return diff, ""
	}
	return "", "Could not compute a diff against base " + base + "; the review relied on reading the checked-out files directly."
}

// fail marks a session failed, persists it, and returns the error.
func (p *Plugin) fail(sess *Session, err error) (string, error) {
	sess.Status = "failed"
	sess.Error = err.Error()
	_ = sess.save(p.cfg.WorkspaceRoot)
	return "", err
}

func applyResult(sess *Session, r *agent.Result) {
	if r == nil {
		return
	}
	sess.Summary = r.FinalText
	sess.Transcript = r.Transcript
	sess.Iterations = r.Iterations
	sess.ToolCalls = r.ToolCalls
	sess.InputTokens = r.InputTokens
	sess.OutputTokens = r.OutputTokens
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func generatedBranch(task string) string {
	slug := slugify(task)
	if slug == "" {
		slug = "task"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return fmt.Sprintf("localdev/%s-%s", slug, time.Now().UTC().Format("0102-1504"))
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func commitMessage(task string, r *agent.Result) string {
	title := prTitle(task)
	body := ""
	if r != nil && strings.TrimSpace(r.FinalText) != "" {
		body = "\n\n" + r.FinalText
	}
	return title + body + "\n\nGenerated by the Squadron LocalDev plugin."
}

func prTitle(task string) string {
	t := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
	if len(t) > 72 {
		t = t[:69] + "..."
	}
	if t == "" {
		t = "LocalDev change"
	}
	return t
}

func prBody(task, instructions string, r *agent.Result) string {
	var b strings.Builder
	b.WriteString("## Task\n\n")
	b.WriteString(task)
	b.WriteString("\n")
	if strings.TrimSpace(instructions) != "" {
		b.WriteString("\n## Instructions\n\n")
		b.WriteString(instructions)
		b.WriteString("\n")
	}
	if r != nil && strings.TrimSpace(r.FinalText) != "" {
		b.WriteString("\n## Summary of changes\n\n")
		b.WriteString(r.FinalText)
		b.WriteString("\n")
	}
	b.WriteString("\n---\nGenerated by the Squadron LocalDev plugin.")
	return b.String()
}

// logf forwards agent progress to the plugin's stderr (visible in Squadron's
// plugin logs) without polluting the tool's stdout result.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[localdev] "+format+"\n", args...)
}
