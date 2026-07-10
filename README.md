# squadron-dev

A [Squadron](https://docs.squadron.sh) plugin that performs **local software
development**. It clones GitHub repositories to your local filesystem and drives a
pluggable Amazon Bedrock provider — the **`bedrock-mantle`** endpoint (OpenAI
Responses or Chat Completions API) by default, or **`bedrock-runtime`** (Converse
API) — through three phases:

1. **Code Development** — implement a change, run tests, open a pull request.
2. **QA** — check out a PR or branch, build and test it, report findings.
3. **Review** — review a change read-only and (optionally) post the review to the PR.

The plugin contains the agent itself: it runs a tool-using loop locally where an
Amazon Bedrock model reads, writes, searches, and executes commands directly in a
sandboxed workspace on disk.

| | LocalDev plugin (this) |
|---|---|
| Where work runs | **Your local machine** |
| The "brain" | **A pluggable Amazon Bedrock provider** |
| Workspace | **Your local filesystem** |
| Repo access | **Local `git` + the GitHub REST API** |

---

## How the requirements map to the design

| Requirement | Where it lives |
|---|---|
| 1. Provider integration via config | [`provider/`](provider/provider.go) interface + registry, with [`provider/mantle/`](provider/mantle/mantle.go) (`bedrock-mantle`, Responses / Chat Completions API) and [`provider/bedrock/`](provider/bedrock/bedrock.go) (`bedrock-runtime`, Converse API) implementations, selected by the `provider` setting |
| 2. Local filesystem as the workspace | [`workspace/`](workspace/workspace.go) — a sandboxed directory; all file I/O and command execution is constrained to it |
| 3. GitHub integration for repository access | [`vcs/`](vcs/git.go) — `git` clone/branch/commit/push + [GitHub REST client](vcs/github.go) for PRs and reviews |
| 4. Accept input direction from the Squadron agent | The `task` / `instructions` tool parameters become the agent [directive](prompts.go); the [agent loop](agent/loop.go) acts on it |
| 5. Three phases: Code Development, QA, Review | [`runner.go`](runner.go) orchestrates each; [`prompts.go`](prompts.go) holds the phase system prompts; tools `code_develop`, `code_qa`, `code_review` |

---

## Two "brains" — don't confuse them

Squadron has its own model providers (Anthropic, OpenAI, Gemini, Ollama) used by the
**orchestrator agent** to decide *which tools to call*. **The plugin's Amazon Bedrock
provider is not one of them.** This plugin uses Bedrock independently, as the brain
that performs the actual local development work, configured in the plugin's own
`settings` block.

```
Squadron orchestrator agent  --(model block: anthropic/openai/...)
        │  calls tool plugins.localdev.code_develop
        ▼
LocalDev plugin  --(provider setting: bedrock-mantle | bedrock-runtime)
        │  runs a local tool-using loop powered by Amazon Bedrock
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
usage, an **Agent Activity** transcript, and the agent's final summary.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | yes | The id returned by a phase tool |

> **Agent Activity in results.** Every phase result (and `workspace_status`) now
> includes an **Agent Activity** section — a bounded, turn-by-turn transcript of the
> agent's responses — so what the model did surfaces in Squadron events, not only in
> the plugin's stderr logs. It's size-capped to avoid flooding the orchestrator's
> context; the complete, untruncated turn log always remains in the plugin logs.

### `cleanup_workspace`

Delete a session's local workspace directory to reclaim disk space.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | yes | The id to delete |

---

## Prerequisites

**To use the plugin** (install from a release):

- **[Squadron](https://docs.squadron.sh)** installed.
- **`git`** on `PATH` (used to clone repos and manage branches locally).
- **Amazon Bedrock access** in your account, with the target model enabled in your
  region. Credentials depend on the provider:
  - `bedrock-mantle` (default) — an **Amazon Bedrock API key**, via the
    `bedrock_api_key` secret or the environment (`AWS_BEARER_TOKEN_BEDROCK` /
    `BEDROCK_API_KEY`).
  - `bedrock-runtime` — a **Bedrock API key** or standard **AWS credentials** (SigV4
    via the credential chain), via `bedrock_api_key` or the environment.
- **A GitHub token** (repo scope) to clone private repos, push branches, and open
  PRs / post reviews — supplied via a Squadron secret (`github_token`) or the
  environment (`GITHUB_TOKEN` / `GH_TOKEN`).

**To build from source** (contributing): **Go 1.23+**. You do *not* need Go to use a
released version — Squadron downloads the prebuilt binary for your platform.

---

## Credentials and security

Both secrets — the **Bedrock API key** and the **GitHub token** — are supplied
through the plugin's `settings`, wired to **Squadron secrets** (encrypted at rest in
Squadron's vault). Neither is hard-coded in HCL, and each falls back to the
environment if the setting is omitted:

```hcl
variable "bedrock_api_key" { secret = true }
variable "github_token"    { secret = true }

plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"
  settings {
    provider        = "bedrock-mantle"
    aws_region      = "us-east-1"
    bedrock_api_key = vars.bedrock_api_key   # secret → Bedrock API key
    github_token    = vars.github_token      # secret → clone / push / PR / review
  }
}
```

Set the secret values once (they are encrypted in `.squadron/vars.vault`):

```bash
squadron vars set bedrock_api_key  "<your-bedrock-api-key>"
squadron vars set github_token     "<your-github-token>"
```

- **`bedrock-mantle` (default).** Authenticates to the mantle endpoint with the
  Amazon Bedrock API key as a bearer token. Set it via `bedrock_api_key`; if omitted
  it falls back to `AWS_BEARER_TOKEN_BEDROCK` / `BEDROCK_API_KEY`. SigV4 is not used
  by this provider — use `bedrock-runtime` if you only have AWS credentials.
- **`bedrock-runtime` — API-key ("API connection") method.** When `bedrock_api_key`
  is set, the AWS SDK client authenticates to the Bedrock Runtime API with an HTTP
  bearer token instead of SigV4. If it is omitted, credentials fall back to the
  standard AWS credential chain (`AWS_ACCESS_KEY_ID`/…, `AWS_PROFILE`, SSO, IAM role,
  or the `AWS_BEARER_TOKEN_BEDROCK` environment variable).
- **API keys.** See the AWS guide for creating an
  [Amazon Bedrock API key](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html).
- **GitHub.** When `github_token` is set it is used for clone/push/PR/review; if
  omitted it falls back to `GITHUB_TOKEN` / `GH_TOKEN` in the environment. On Unix
  the token is supplied to `git` through a transient `GIT_ASKPASS` helper via the
  child process environment, so it never appears in the process argument list or in
  the repository's `git` config. In all cases credentials are **redacted** from any
  error message before it is shown or persisted.

See [`.env.example`](.env.example) for the environment-fallback form. Without a
GitHub token the plugin still operates on public repositories read-only; develop
runs commit locally but skip push/PR, and review returns its result without posting.

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

## Installation

Install the plugin straight from its GitHub releases by referencing it by **module
path and version** in your Squadron config. On first use Squadron downloads the
prebuilt binary for your platform, verifies it against the release's `checksums.txt`,
and caches it — there is no local build step.

```hcl
plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"   # a published release tag
}
```

Verify the install and list the plugin's tools:

```bash
squadron plugin tools localdev -v v0.1.0
```

`version` must be a published release tag (see [Releasing](#releasing) for how tags
become installable releases). Use the newest tag from the repository's releases.

### Building from source (contributors)

You only need this to develop the plugin itself. Point `source` at a local checkout
and use `version = "local"` so Squadron rebuilds it on config load:

```hcl
plugin "localdev" {
  source  = "./squadron-dev"   # path to your checkout, relative to the HCL file
  version = "local"
}
```

Or build and install the binary manually:

```bash
git clone https://github.com/ericlakich/squadron-dev.git
cd squadron-dev
go test ./...
go build -o plugin .
mkdir -p ~/.squadron/plugins/localdev/local
cp plugin ~/.squadron/plugins/localdev/local/plugin
```

---

## Configuration

Configure the plugin with a `settings` block. A typical setup:

```hcl
plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"

  settings {
    provider        = "bedrock-mantle"
    aws_region      = "us-east-1"
    bedrock_api_key = vars.bedrock_api_key
    model_id        = "openai.gpt-oss-120b"
    max_tokens      = "8192"

    max_iterations          = "60"
    command_timeout_seconds = "600"

    git_user_name  = "Squadron LocalDev"
    git_user_email = "localdev@squadron.sh"
    auto_push      = "true"
    open_pr        = "true"
  }
}
```

> For local plugin development, swap `source`/`version` for the local form shown in
> [Installation](#installation) above — the `settings` block is identical.

### Providers

The plugin ships two Amazon Bedrock providers, selected with the `provider` setting.
Both drive the same local agent loop; they differ in which Bedrock endpoint and API
they use and how they authenticate.

| | `bedrock-mantle` (default) | `bedrock-runtime` |
|---|---|---|
| Endpoint | `bedrock-mantle.{region}.api.aws` | `bedrock-runtime.{region}.amazonaws.com` |
| API | OpenAI-compatible **Responses** or **Chat Completions** (`mantle_api`) | AWS **Converse** API (via the AWS SDK) |
| Transport | Direct HTTPS (`net/http`) | AWS SDK for Go v2 |
| Auth | Amazon Bedrock API key (bearer) | Bedrock API key **or** AWS SigV4 credential chain |
| Model id form | `openai.gpt-oss-120b`, `qwen.qwen3-coder-next`, … | inference profile, e.g. `us.anthropic.claude-sonnet-4-20250514-v1:0` |
| Best for | New setups; AWS recommends this endpoint | Existing AWS SDK / SigV4 setups, invocation logging |

`bedrock` is accepted as a backward-compatible alias for `bedrock-runtime`.

**Choosing the `bedrock-mantle` API (`mantle_api`).** The mantle endpoint exposes
two OpenAI-compatible APIs, and **model support differs**:

- `responses` (default) — the OpenAI **Responses** API (`POST /v1/responses`).
  Supported only by the OpenAI GPT family (e.g. `gpt-oss-120b`, `gpt-oss-20b`).
- `chat_completions` — the OpenAI **Chat Completions** API
  (`POST /v1/chat/completions`). Broadly supported, including **Qwen**
  (`qwen.qwen3-coder-next`), which does **not** support the Responses API.

If you point `bedrock-mantle` at a model that doesn't support the selected API you
get an HTTP 400 like `The model '…' does not support the '/v1/responses' API` — set
`mantle_api = "chat_completions"` (or switch to `bedrock-runtime`) for that model.

> **Model ids and API support differ.** Set `model_id` to a model that supports the
> API you selected. Check
> [model/API compatibility](https://docs.aws.amazon.com/bedrock/latest/userguide/models-api-compatibility.html)
> and verify the exact id before relying on it.

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

The settings are detailed below, and several ready-to-adapt configs follow in
[Configuration examples](#configuration-examples).

### Settings reference

| Setting | Default | Description |
|---------|---------|-------------|
| `provider` | `bedrock-mantle` | Bedrock provider that powers the local agent: `bedrock-mantle` (mantle endpoint) or `bedrock-runtime` (Converse API). `bedrock` is an alias for `bedrock-runtime`. See [Providers](#providers). |
| `mantle_api` | `responses` | `bedrock-mantle` only. Which mantle API to use: `responses` (OpenAI Responses; OpenAI GPT models only) or `chat_completions` (OpenAI Chat Completions; broadly supported, incl. Qwen). |
| `bedrock_api_key` | _(env fallback)_ | **Secret.** Amazon Bedrock API key. Wire to a Squadron secret. Required for `bedrock-mantle`; for `bedrock-runtime` it enables bearer-token auth. If unset, falls back to `AWS_BEARER_TOKEN_BEDROCK` / `BEDROCK_API_KEY` (and, for `bedrock-runtime`, the full AWS credential chain). |
| `github_token` | _(env fallback)_ | **Secret.** GitHub token for clone/push/PR/review. Wire to a Squadron secret. If unset, falls back to `GITHUB_TOKEN` / `GH_TOKEN`. |
| `aws_region` | `us-east-1` | AWS region. Selects the Bedrock endpoint host for both providers. |
| `mantle_endpoint` | _(derived from region)_ | `bedrock-mantle` only. Full base URL override, e.g. `https://bedrock-mantle.us-east-1.api.aws`. Env fallback `BEDROCK_MANTLE_ENDPOINT`. |
| `aws_profile` | _(none)_ | `bedrock-runtime` only. Shared-config profile name (credential-chain auth). |
| `model_id` | `openai.gpt-oss-120b` (mantle) / `us.anthropic.claude-sonnet-4-20250514-v1:0` (runtime) | Model id. The form differs by provider — see [Providers](#providers). |
| `max_tokens` | `8192` | Max output tokens per model turn (`max_output_tokens` on the Responses API). |
| `temperature` | _(model default)_ | Sampling temperature. On `bedrock-mantle` it is sent only when set here, since some Responses models reject an explicit temperature. |
| `request_timeout_seconds` | `300` | Max wall-clock time for a **single model call**. Bounds a hung or half-open Bedrock connection so it fails with a clear error instead of blocking. Raise it if a model legitimately needs longer per turn. |
| `workspace_root` | `~/.squadron/localdev/workspaces` | Root directory for per-session workspaces. |
| `max_iterations` | `50` | Max agent loop turns per phase before stopping. |
| `command_timeout_seconds` | `300` | Per-command timeout for the agent's `run_command` **subprocesses only** — it does not bound model calls (see `request_timeout_seconds`). |
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

> `bedrock_api_key` and `github_token` are secrets — wire them to Squadron secrets
> rather than hard-coding them, or leave them unset to use the environment. See
> [Credentials and security](#credentials-and-security).

### Configuration examples

**Minimal** — rely on the defaults (full clone, all three phases, full repo
context) and grant the agent every tool with the `.all` wildcard:

```hcl
model "anthropic" {
  provider = "anthropic"
  api_key  = vars.anthropic_api_key
}

plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"
  settings {
    provider        = "bedrock-mantle"
    aws_region      = "us-east-1"
    bedrock_api_key = vars.bedrock_api_key
  }
}

agent "engineer" {
  model = models.anthropic.claude_sonnet_4
  tools = [plugins.localdev.all]
}
```

**Develop-only agent** — implements changes and opens PRs; no QA/review tools:

```hcl
agent "implementer" {
  model       = models.anthropic.claude_sonnet_4
  personality = "Ships small, well-tested changes and opens clean PRs."
  tools       = [plugins.localdev.code_develop, plugins.localdev.workspace_status]
}
```

**Review bot** — reviews PRs read-only and posts the review back to GitHub. Call
`code_review` with `post_comments = true` to publish it:

```hcl
agent "reviewer" {
  model       = models.anthropic.claude_opus_4
  personality = "A thorough, security-minded senior reviewer."
  tools       = [plugins.localdev.code_review]
}
```
```json
{ "pr_url": "https://github.com/org/repo/pull/42", "post_comments": true }
```

**Large monorepo** — shallow clone for speed, bigger budgets, push the branch but
leave PR creation to a human:

```hcl
plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"
  settings {
    provider                = "bedrock-mantle"
    aws_region              = "us-east-1"
    bedrock_api_key         = vars.bedrock_api_key
    model_id                = "openai.gpt-oss-120b"
    clone_depth             = "1"
    max_iterations          = "120"
    command_timeout_seconds = "900"
    max_context_bytes       = "65536"
    open_pr                 = "false"
  }
}
```

**Qwen Coder on `bedrock-mantle`** — Qwen supports Chat Completions but not the
Responses API, so select `mantle_api = "chat_completions"`:

```hcl
plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"
  settings {
    provider        = "bedrock-mantle"
    mantle_api      = "chat_completions"
    aws_region      = "us-west-2"
    bedrock_api_key = vars.bedrock_api_key
    model_id        = "qwen.qwen3-coder-next"
    max_tokens      = "65536"
    temperature     = "0"
  }
}
```

**Converse via `bedrock-runtime`** — use the AWS SDK path with a named AWS profile
(SigV4) and a Claude Opus inference profile in `us-west-2`:

```hcl
plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"
  settings {
    provider    = "bedrock-runtime"
    aws_region  = "us-west-2"
    aws_profile = "bedrock-prod"
    model_id    = "us.anthropic.claude-opus-4-20250514-v1:0"
    temperature = "0"
  }
}
```

A complete, runnable develop → QA → review mission — including the orchestrator
model, the plugin block, an agent, optional skills, and a three-task mission — is in
[`examples/squadron.hcl`](examples/squadron.hcl). A bare-minimum config is in
[`examples/minimal.hcl`](examples/minimal.hcl).

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
  examples/           # Example Squadron HCL configs (minimal.hcl, squadron.hcl)
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
