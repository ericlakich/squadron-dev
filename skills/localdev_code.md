# LocalDev — Code Development Phase

Use the LocalDev plugin to perform software development on the **local machine**. A
provider-backed coding agent (AWS Bedrock) clones a GitHub repository to the local
filesystem, implements the requested change, runs builds and tests locally, then
commits the work to a new branch, pushes it, and opens a pull request.

## When to use

Use `code_develop` for feature work, bug fixes, refactors, or any change that
requires editing a repository and opening a PR.

## `code_develop`

**Required parameters**
- `repo_url` — full GitHub repository URL (e.g. `https://github.com/org/repo`)
- `task` — clear description of what to implement. This is the direction the agent
  follows, so be specific.

**Optional parameters**
- `branch` — name for the branch to create. If omitted, a name is generated from the task.
- `base_branch` — branch to start from and target with the PR. Defaults to the repo default branch.
- `instructions` — additional context, constraints, coding conventions, or acceptance criteria.

**Writing an effective task**
- State precisely what to change and where in the codebase.
- Reference existing patterns or files to follow.
- Include acceptance criteria so the agent knows when it is done.
- Call out test requirements and conventions in `instructions`.

**Example**
```json
{
  "repo_url": "https://github.com/org/repo",
  "task": "Add cursor-based pagination to the GET /users API endpoint",
  "branch": "feature/users-pagination",
  "instructions": "Follow the pagination pattern in the /orders endpoint. Add unit tests and run `go test ./...` before finishing."
}
```

The response includes the session id, repository, branch, the pull request URL (if
opened), token usage, and the agent's own summary of what it changed and how it
verified the work.

## What happens locally

1. The repo is cloned into a local workspace directory.
2. A fresh branch is created from the base branch.
3. The agent explores the repo and uses local tools — read, write, edit, search,
   and run shell commands (build, test, lint, git) — until the task is complete.
4. The plugin commits the changes, pushes the branch, and opens a PR.

If the agent makes no file changes, the response says so and no PR is opened.
If no `GITHUB_TOKEN` is present in the environment, work is committed locally but
push and PR creation are skipped (the response notes this).

## Following up

- Inspect a finished session later with `workspace_status` (pass the `session_id`).
- Reclaim disk space with `cleanup_workspace` when results are no longer needed.
- Run `code_qa` and `code_review` against the resulting PR to complete the
  development → QA → review cycle.
