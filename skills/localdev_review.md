# LocalDev — Review Phase

Use the LocalDev plugin to perform a code review on the **local machine**. The agent
checks out a pull request or branch into a local workspace, gathers its diff, and
reviews the change read-only — it cannot modify, run, commit, or push anything.
Optionally, the review is posted back to the GitHub PR.

## When to use

Use `code_review` as the final phase, after QA, to judge code quality and decide
whether a change should be approved.

## `code_review`

Provide **either** a pull request or a repository + branch.

**Parameters**
- `pr_url` — full GitHub pull request URL to review
- `repo_url` — repository URL, when reviewing a branch rather than a PR
- `branch` — branch to review when using `repo_url`
- `base_branch` — base branch to diff a branch against (defaults to the default branch)
- `instructions` — optional focus areas (e.g. "check for security vulnerabilities")
- `post_comments` — if `true` and reviewing a PR, post the review as a comment on the
  GitHub PR (requires `GITHUB_TOKEN`)

**Example**
```json
{
  "pr_url": "https://github.com/org/repo/pull/123",
  "instructions": "Check for security vulnerabilities and adherence to repo conventions.",
  "post_comments": true
}
```

## What the review covers

- Correctness and potential bugs
- Code quality, readability, and maintainability
- Security concerns and vulnerabilities
- Adherence to repository conventions and best practices
- Test adequacy

The agent reads surrounding files for context, not just the diff. Its summary
includes an overall recommendation (approve / comment / request changes), specific
findings citing file and region, and a short summary suitable for a single PR
review comment.

When `post_comments` is `true`, that review is posted to the PR as a `COMMENT`
review event. Without a `GITHUB_TOKEN`, the review is returned in the response but
not posted (the response notes this).
