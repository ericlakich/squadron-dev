# LocalDev — QA Phase

Use the LocalDev plugin to QA a change on the **local machine**. The agent checks
out a pull request or branch into a local workspace, builds the project, runs the
test suite, and reports findings. It is read-only with respect to the source: it
may run commands but does not modify, commit, or push code.

## When to use

Use `code_qa` after a change is implemented (for example, the PR opened by
`code_develop`) to validate it before review or merge.

## `code_qa`

Provide **either** a pull request or a repository + branch.

**Parameters**
- `pr_url` — full GitHub pull request URL (e.g. `https://github.com/org/repo/pull/123`)
- `repo_url` — repository URL, when QAing a branch rather than a PR
- `branch` — branch to check out when using `repo_url` (defaults to the default branch)
- `instructions` — optional focus areas (e.g. "focus on error handling and concurrency")

**Example**
```json
{
  "pr_url": "https://github.com/org/repo/pull/123",
  "instructions": "Focus on edge cases and missing test coverage for the new pagination code."
}
```

## What the QA pass covers

- Building the project and running the existing test suite, reporting failures verbatim.
- Bugs, edge cases, logic errors, and inadequate error handling.
- Missing test coverage for new or changed behavior.
- Regressions in related functionality and performance concerns.
- Whether the change matches its stated intent.

## Interpreting the report

The agent's summary is grouped into:
- **Critical issues** — must fix
- **Warnings** — should fix
- **Suggestions** — nice to have
- **Test results** — what was run and the outcome
- **Looks good** — what is solid

Use `workspace_status` with the returned `session_id` to re-read the report later.
