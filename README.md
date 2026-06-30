# squadron-dev

A [Squadron](https://docs.squadron.sh) plugin that performs **local software
development**. It clones GitHub repositories to your local filesystem and drives a
pluggable LLM provider — **AWS Bedrock** to start — through three phases:

1. **Code Development** — implement a change, run tests, open a pull request.
2. **QA** — check out a PR or branch, build and test it, report findings.
3. **Review** — review a change read-only and (optionally) post the review to the PR.

It is modeled on the [`squadron-plugin-devin`](https://github.com/ericlakich/squadron-plugin-devin)
reference plugin, with one fundamental difference:

| | Devin plugin | LocalDev plugin (this) |
|---|---|---|
| Where work runs | Devin's remote cloud | **Your local machine** |
| The "brain" | Devin's hosted agent | **A pluggable provider (AWS Bedrock)** |
| Workspace | Devin-managed | **Your local filesystem** |
| Repo access | Devin's GitHub integration | **Local `git` + the GitHub REST API** |

The Devin plugin is a thin HTTP client to a remote agent. This plugin contains the
agent: it runs a tool-using loop locally where the model reads, writes, searches,
and executes commands directly in a sandboxed workspace on disk.

---

## How the requirements map to the design

| Requirement | Where it lives |
|---|---|
| 1. Provider integration via config (AWS Bedrock first/only) | [`provider/`](provider/provider.go) interface + registry, [`provider/bedrock/`](provider/bedrock/bedrock.go) implementation, selected by the `provider` setting |
| 2. Local filesystem as the workspace | [`workspace/`](workspace/workspace.go) — a sandboxed directory; all file I/O and command execution is constrained to it |
| 3. GitHub integration for repository access | [`vcs/`](vcs/git.go) — `git` clone/branch/commit/push + [GitHub REST client](vcs/github.go) for PRs and reviews |
| 4. Accept input direction from the Squadron agent | The `task` / `instructions` tool parameters become the agent [directive](prompts.go); the [agent loop](agent/loop.go) acts on it |
| 5. Three phases: Code Development, QA, Review | [`runner.go`](runner.go) orchestrates each; [`prompts.go`](prompts.go) holds the phase system prompts; tools `code_develop`, `code_qa`, `code_review` |

---

## Two "brains" — don't confuse them

Squadron has its own model providers (Anthropic, OpenAI, Gemini, Ollama) used by the
**orchestrator agent** to decide *which tools to call*. **AWS Bedrock is not one of
them.** This plugin uses Bedrock independently, as the brain that performs the actual
local development work, configured in the plugin's own `settings` block — exactly as
the Devin plugin configures Devin as its backend.

```
Squadron orchestrator agent  --(model block: anthropic/openai/...)
        │  calls tool plugins.localdev.code_develop
        ▼
LocalDev plugin  --(provider setting: bedrock)
        │  runs a local tool-using loop powered by Bedrock
        ▼
Local filesystem workspace + git + GitHub
```

---

## Tools

### `code_develop` — Code Development phase

Clone a repo locally, drive the agent to implement `task`, then commit to a new
branch, push, and open a pull request.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `repo_url` | string | yes | Full GitHub repo URL (`https://github.com/org/repo`) |
| `task` | string | yes | What to implement — the direction for the agent |
| `branch` | string | no | Branch to create. Generated from the task if omitted. |
| `base_branch` | string | no | Branch to start from / target with the PR. Defaults to the repo default branch. |
| `instructions` | string | no | Extra context, constraints, or coding guidelines |

### `code_qa` — QA phase

Check out a PR or branch locally, build it, run tests, and report. Read-only with
respect to the source (it can run commands but not modify/commit/push code).

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `pr_url` | string | one of | Full GitHub PR URL (`.../pull/123`) |
| `repo_url` | string | one of | Repo URL, when QAing a branch instead of a PR |
| `branch` | string | no | Branch to check out with `repo_url` |
| `instructions` | string | no | Focus areas / additional instructions |

### `code_review` — Review phase

Check out a PR or branch, gather its diff, review read-only, and optionally post the
review to the GitHub PR.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `pr_url` | string | one of | Full GitHub PR URL |
| `repo_url` | string | one of | Repo URL, when reviewing a branch |
| `branch` | string | no | Branch to review with `repo_url` |
| `base_branch` | string | no | Base to diff a branch against |
| `instructions` | string | no | Focus areas / additional instructions |
| `post_comments` | boolean | no | If true and reviewing a PR, post the review to GitHub (needs `GITHUB_TOKEN`) |

### `workspace_status`

Inspect a session created by any phase. Returns status, repo, branch, PR, token
usage, and the agent's final summary.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | yes | The id returned by a phase tool |

### `cleanup_workspace`

Delete a session's local workspace directory to reclaim disk space.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | yes | The id to delete |

---

## Prerequisites

- **Go 1.23+** to build the plugin.
- **`git`** on `PATH` (used for clone/branch/commit/push).
- **AWS Bedrock access** in your account, with the target model/inference profile
  enabled in your region, and credentials available via the standard AWS credential
  chain (env vars, shared profile, SSO, or an IAM role).
- **A GitHub token** in the environment (`GITHUB_TOKEN` or `GH_TOKEN`) to clone
  private repos, push branches, and open PRs / post reviews.

---

## Credentials and security

Credentials are **never** read from HCL settings:

- **AWS Bedrock** credentials come from the standard AWS credential chain
  (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, SSO, or IAM role).
  Settings only pick the region, model, and inference parameters.
- **GitHub** authentication comes from the `GITHUB_TOKEN` (or `GH_TOKEN`) environment
  variable. On Unix the token is supplied to `git` through a transient `GIT_ASKPASS`
  helper via the child process environment, so it never appears in the process
  argument list or in the repository's `git` config. (On other platforms it is
  injected into the remote URL for the single network call as a fallback.) In all
  cases credentials are **redacted** from any error message before it is shown or
  persisted.

See [`.env.example`](.env.example). Without a GitHub token the plugin still operates
on public repositories read-only; develop runs commit locally but skip push/PR, and
review returns its result without posting.

> **Trust model.** During `code_develop` and `code_qa` the agent can run shell
> commands the model chooses (builds, tests, linters, package installs) inside the
> workspace directory, bounded by `command_timeout_seconds`. Run untrusted tasks in
> an isolated environment (Squadron supports Docker), and scope the GitHub token to
> only the repositories you intend the plugin to touch.
>
> **Sandbox.** All file reads and writes are confined to the session workspace. The
> workspace rejects both lexical (`..`) and symlink-based escapes — a checked-in
> symlink in an untrusted repository cannot be used to read or write outside the
> root — and refuses writes into the `.git` directory so the agent cannot tamper
> with git hooks or config.

---

## Build

```bash
git clone https://github.com/ericlakich/squadron-dev.git
cd squadron-dev

go mod tidy
go test ./...
go build -o plugin .
```

For Squadron local development, place the binary where Squadron expects it, or point
the HCL `source` at the project directory and let Squadron auto-build (see below).

```bash
# Manual install into Squadron's local plugin directory:
mkdir -p ~/.squadron/plugins/localdev/local
cp plugin ~/.squadron/plugins/localdev/local/plugin
```

---

## Configuration

Add the plugin to your Squadron HCL config. For local development, point `source` at
the project and use `version = "local"` so Squadron rebuilds on config load:

```hcl
plugin "localdev" {
  source  = "./squadron-dev"
  version = "local"

  settings {
    provider    = "bedrock"
    aws_region  = "us-east-1"
    model_id    = "us.anthropic.claude-sonnet-4-20250514-v1:0"
    max_tokens  = "8192"
    temperature = "0"

    max_iterations          = "60"
    command_timeout_seconds = "600"

    git_user_name  = "Squadron LocalDev"
    git_user_email = "localdev@squadron.sh"
    auto_push      = "true"
    open_pr        = "true"
  }
}
```

Attach the tools to an agent:

```hcl
agent "engineer" {
  model       = models.anthropic.claude_sonnet_4
  personality = "A pragmatic senior engineer who ships small, well-tested changes."
  tools = [
    plugins.localdev.code_develop,
    plugins.localdev.code_qa,
    plugins.localdev.code_review,
    plugins.localdev.workspace_status,
    plugins.localdev.cleanup_workspace,
  ]
}
```

A complete develop → QA → review mission is in [`examples/squadron.hcl`](examples/squadron.hcl).

### Settings reference

| Setting | Default | Description |
|---------|---------|-------------|
| `provider` | `bedrock` | LLM provider that powers the local agent. `bedrock` is the only built-in. |
| `aws_region` | `us-east-1` | AWS region for Bedrock. |
| `aws_profile` | _(none)_ | Optional shared-config profile name. |
| `model_id` | `us.anthropic.claude-sonnet-4-20250514-v1:0` | Bedrock model id or inference profile id. |
| `max_tokens` | `8192` | Max output tokens per model turn. |
| `temperature` | `0` | Sampling temperature. |
| `workspace_root` | `~/.squadron/localdev/workspaces` | Root directory for per-session workspaces. |
| `max_iterations` | `50` | Max agent loop turns per phase before stopping. |
| `command_timeout_seconds` | `300` | Per-command timeout for `run_command`. |
| `max_output_bytes` | `60000` | Cap on file/command output returned to the model. |
| `clone_depth` | `0` | Git clone depth. `0` = **full clone** (complete working tree and history). Set a positive integer for a faster shallow clone of very large repositories. |
| `load_repo_context` | `true` | Load the repository's own instruction/convention files into the agent's context (see [Full-codebase access](#full-codebase-access)). |
| `context_files` | _(curated set)_ | Comma-separated override of which instruction files to load, relative to the repo root. |
| `max_context_bytes` | `32768` | Cap on the total size of loaded instruction-file context. |
| `git_user_name` | `Squadron LocalDev` | Commit author name. |
| `git_user_email` | `localdev@squadron.sh` | Commit author email. |
| `auto_push` | `true` | Push the branch after a successful develop run. |
| `open_pr` | `true` | Open a PR after pushing (develop phase). |
| `base_branch` | _(repo default)_ | Default base branch when a phase does not specify one. |

> Credentials (`AWS_*`, `GITHUB_TOKEN`) are intentionally **not** settings — see
> [Credentials and security](#credentials-and-security).

---

## Full-codebase access

The plugin is built to operate as an autonomous agent over a **whole repository**,
not a partial slice:

- **Full clone.** By default the repository is cloned with full history
  (`clone_depth = 0`), so the agent has the complete working tree and can use
  `git log`, `git blame`, and accurate base diffs. Set `clone_depth` to a positive
  value to trade completeness for clone speed on very large repos.
- **The repo's own instructions become context.** Before each phase runs, the
  plugin reads the repository's standard instruction and convention files from the
  checkout and injects them into the agent's system prompt as authoritative
  guidance, so the agent follows the project's documented build/test commands,
  style, and rules. Loaded in priority order (first match wins under the byte
  budget):

  | File | Source convention |
  |------|-------------------|
  | `AGENTS.md` | the `agents.md` cross-tool standard |
  | `CLAUDE.md` | Claude Code |
  | `GEMINI.md` | Gemini CLI |
  | `.cursorrules`, `.cursor/rules/*.mdc` | Cursor |
  | `.windsurfrules` | Windsurf |
  | `.github/copilot-instructions.md` | GitHub Copilot |
  | `.goosehints` | Goose |
  | `CONTRIBUTING.md`, `README.md` | human-facing project docs |

  Override the list with `context_files`, bound the total size with
  `max_context_bytes`, or disable entirely with `load_repo_context = "false"`. The
  agent can always read any other file (including nested `AGENTS.md`/`CLAUDE.md`)
  on demand with its `read_file` tool.

## How it works

Each phase creates a **session**: a directory under `workspace_root` containing a
`session.json` manifest and a `repo/` checkout. The session id is returned in the
result and can be passed to `workspace_status` and `cleanup_workspace`.

**Code Development (`code_develop`)**
1. Clone the repo into the session workspace and create a branch off the base.
2. Run the agent loop with read/write/search and `run_command` tools. The model
   explores, edits files, builds, and runs tests until done.
3. Commit all changes, push the branch, and open a pull request.

**QA (`code_qa`)**
1. Clone and check out the PR (`refs/pull/<n>/head`) or the requested branch.
2. Run the agent loop with read/search and `run_command` (no write). The model
   builds the project, runs tests, and analyzes the change.
3. Return a structured QA report. Nothing is written back.

**Review (`code_review`)**
1. Clone and check out the change; fetch the unified diff (GitHub API for PRs, local
   `git diff` for branches).
2. Run the agent loop read-only (read/search; no commands). The diff is embedded in
   the directive and the model reads surrounding files for context.
3. Return the review and, if `post_comments` is set for a PR, post it as a review
   comment.

### The agent loop

[`agent.Run`](agent/loop.go) implements a standard tool-use loop: send the
conversation + tool specs to the provider; if the model requests tools, execute each
against the workspace and feed the results back; repeat until the model stops calling
tools (`end_turn`) or `max_iterations` is reached. The model's final message is the
phase summary. The loop respects context cancellation, so Squadron can terminate a
long-running call cleanly.

---

## Adding another provider

The provider is an interface, so adding (say) OpenAI or Anthropic-direct is small:

1. Implement [`provider.Provider`](provider/provider.go) (`Name` + `Converse`).
2. Call `provider.Register("yourname", New)` from your package's `init`.
3. Blank-import the package in [`main.go`](main.go) so it registers.
4. Select it with `provider = "yourname"` in settings.

`Converse` takes a neutral request (system prompt, messages, tool specs, inference
params) and returns the model's text and any tool-use requests — the agent loop and
all phases are provider-agnostic.

---

## Project structure

```
squadron-dev/
  main.go             # Entry point: squadron.Serve(&Plugin{}); registers providers
  plugin.go           # ToolProvider impl: tool schemas, Configure, Call dispatch
  config.go           # Settings parsing (credentials come from env, not settings)
  session.go          # Per-session manifest + workspace layout
  prompts.go          # Phase system prompts and directive builders
  runner.go           # Orchestration for each phase (clone → loop → commit/PR)
  format.go           # Human-readable result formatting

  provider/
    provider.go       # Provider interface + registry (neutral message/tool model)
    bedrock/
      bedrock.go      # AWS Bedrock Converse API implementation

  workspace/
    workspace.go      # Sandboxed local filesystem workspace + command execution

  agent/
    loop.go           # The tool-using agent loop
    tools.go          # Local tools: read/write/edit/list/search/run_command

  vcs/
    git.go            # git clone/branch/commit/push/diff over os/exec
    github.go         # GitHub REST client: PRs, reviews, URL parsing

  skills/             # Phase guidance, loadable as Squadron skills via load()
  examples/           # Example Squadron HCL config
  .github/workflows/  # Release workflow (cross-platform build + checksums)
```

---

## Testing

```bash
go test ./...
```

Tests cover the agent loop (with a fake provider, including tool dispatch, error
handling, and the iteration cap), the workspace file/command behavior and sandbox
(lexical **and** symlink escape rejection, `.git`-write blocking), repository-context
gathering (priority order and byte budget), GitHub URL parsing and credential
redaction, the Bedrock document round-trip and stop-reason mapping, and a git
integration test against a local `file://` remote (clone, checkout including the
ref==current-branch case, commit, push, and diff).

---

## Releasing

Tag-published releases build cross-platform binaries (darwin/linux/windows ×
amd64/arm64), generate `checksums.txt`, and upload assets named
`squadron-dev_<os>_<arch>.<ext>` — the layout Squadron expects to resolve
a plugin from `source = "github.com/ericlakich/squadron-dev"` +
`version = "vX.Y.Z"`. See [`.github/workflows/release.yml`](.github/workflows/release.yml).

```bash
# Cut a release: create a GitHub release with a vX.Y.Z tag; the workflow does the rest.
git tag v0.1.0
git push origin v0.1.0
```

---

## License

[MIT](LICENSE).
