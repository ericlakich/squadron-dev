# Example Squadron configuration that uses the LocalDev plugin to run a
# develop -> QA -> review pipeline. This is illustrative; adapt names and ids.
#
# Two distinct "brains" are involved, and it is important not to confuse them:
#
#   1. The Squadron orchestrator model (the `model` block below) reasons about
#      which plugin tools to call. Squadron supports anthropic, openai, gemini,
#      and ollama here. Bedrock is NOT a Squadron model provider.
#
#   2. The LocalDev plugin's own provider (configured in the plugin `settings`)
#      is the brain that performs the local development work. That is where
#      AWS Bedrock is selected.

variable "anthropic_api_key" {
  type = string
}

# (1) The orchestrator model that decides when to call the plugin's tools.
model "anthropic" {
  provider = "anthropic"
  api_key  = vars.anthropic_api_key
}

# (2) The LocalDev plugin, installed from a published GitHub release (recommended):
# Squadron downloads, checksum-verifies, and caches the prebuilt binary — no local
# build. For local plugin development instead, use a checkout path and "local":
#     source = "./squadron-dev"   version = "local"
plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"

  settings {
    # Provider selection (Bedrock is the default and currently only provider).
    provider    = "bedrock"
    aws_region  = "us-east-1"
    model_id    = "us.anthropic.claude-sonnet-4-20250514-v1:0"
    max_tokens  = "8192"
    temperature = "0"

    # Local agent loop budget and command execution.
    max_iterations          = "60"
    command_timeout_seconds = "600"

    # Full-codebase access. clone_depth = "0" is a full clone (complete tree and
    # history); the repo's own AGENTS.md/CLAUDE.md/.cursorrules/etc. are loaded as
    # context automatically.
    clone_depth        = "0"
    load_repo_context  = "true"
    max_context_bytes  = "32768"
    # context_files    = "AGENTS.md,CLAUDE.md,CONTRIBUTING.md"   # optional override

    # Git / PR behavior.
    git_user_name = "Squadron LocalDev"
    git_user_email = "localdev@squadron.sh"
    auto_push      = "true"
    open_pr        = "true"

    # NOTE: no credentials here. AWS comes from the credential chain; the GitHub
    # token comes from the GITHUB_TOKEN environment variable.
  }
}

# An agent that can run all three phases plus session management.
agent "engineer" {
  model       = models.anthropic.claude_sonnet_4
  personality = "A pragmatic senior engineer who ships small, well-tested changes and reviews thoroughly."
  tools = [
    plugins.localdev.code_develop,
    plugins.localdev.code_qa,
    plugins.localdev.code_review,
    plugins.localdev.workspace_status,
    plugins.localdev.cleanup_workspace,
  ]
}

# Optional skills. Squadron skills are authored in your config (plugins ship tools,
# not skills), and `load()` reads local files. The plugin repo's skills/*.md are
# provided to use here: copy them next to this config as ./skills/, or inline the
# instructions. Skills are optional — the plugin's tool descriptions already guide
# usage, so you can delete these blocks and rely on `tools = [...]` alone.
skill "localdev_develop" {
  description  = "Load when implementing a code change on a repository and opening a PR"
  instructions = load("./skills/localdev_code.md")
  tools        = [plugins.localdev.code_develop, plugins.localdev.workspace_status]
}

skill "localdev_qa" {
  description  = "Load when QAing a pull request or branch"
  instructions = load("./skills/localdev_qa.md")
  tools        = [plugins.localdev.code_qa]
}

skill "localdev_review" {
  description  = "Load when reviewing a pull request or branch"
  instructions = load("./skills/localdev_review.md")
  tools        = [plugins.localdev.code_review]
}

mission "ship_feature" {
  commander { model = models.anthropic.claude_sonnet_4 }

  agents = [agents.engineer]

  task "develop" {
    objective = "Use code_develop on ${inputs.repo_url} to: ${inputs.task}"
  }
  task "qa" {
    objective = "Use code_qa to validate the pull request opened in the develop task."
  }
  task "review" {
    objective = "Use code_review to review the pull request and post the review with post_comments=true."
  }
}
