# Minimal Squadron config using the LocalDev plugin.
# Installs the plugin from a published GitHub release and gives one agent every
# LocalDev tool. Everything else uses defaults (full clone, repo-context loading,
# auto push + PR).
#
# Secrets are supplied via Squadron's vault and wired into the plugin settings.
# Set them once:
#   squadron vars set bedrock_api_key "<your-bedrock-api-key>"
#   squadron vars set github_token    "<your-github-token>"
#   squadron vars set anthropic_api_key "<your-anthropic-api-key>"

variable "anthropic_api_key" { secret = true }
variable "bedrock_api_key"   { secret = true }
variable "github_token"      { secret = true }

# The orchestrator model that decides which plugin tools to call.
model "anthropic" {
  provider = "anthropic"
  api_key  = vars.anthropic_api_key
}

# The LocalDev plugin, installed from a release. Bump `version` to the latest tag.
plugin "localdev" {
  source  = "github.com/ericlakich/squadron-dev"
  version = "v0.1.0"

  settings {
    provider        = "bedrock-mantle"       # Responses API (default)
    aws_region      = "us-east-1"
    bedrock_api_key = vars.bedrock_api_key    # secret → Amazon Bedrock API key
    github_token    = vars.github_token       # secret → clone / push / PR / review
    # model_id defaults to openai.gpt-oss-120b; set a Responses-capable model id.
  }
}

# One agent with all of the plugin's tools.
agent "engineer" {
  model       = models.anthropic.claude_sonnet_4
  personality = "A pragmatic senior engineer who ships small, well-tested changes and reviews thoroughly."
  tools       = [plugins.localdev.all]
}
