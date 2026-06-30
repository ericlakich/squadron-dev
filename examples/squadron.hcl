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

# (2) The LocalDev plugin. Build it locally (see the README) and point `source`
# at the build output, or reference a published release by source + version.
plugin "localdev" {
  source  = "./squadron-plugin-localdev"
  version = "local"

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

# Optional: package the phase guidance shipped with the plugin as skills the
# agent can load. The skill markdown files live in this plugin's skills/ folder.
skill "localdev_develop" {
  description  = "Load when implementing a code change on a repository and opening a PR"
  instructions = load("./squadron-plugin-localdev/skills/localdev_code.md")
  tools        = [plugins.localdev.code_develop, plugins.localdev.workspace_status]
}

skill "localdev_qa" {
  description  = "Load when QAing a pull request or branch"
  instructions = load("./squadron-plugin-localdev/skills/localdev_qa.md")
  tools        = [plugins.localdev.code_qa]
}

skill "localdev_review" {
  description  = "Load when reviewing a pull request or branch"
  instructions = load("./squadron-plugin-localdev/skills/localdev_review.md")
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
