# Minimal Squadron config using the LocalDev plugin.
# Installs the plugin from a published GitHub release and gives one agent every
# LocalDev tool. Everything else uses defaults (full clone, repo-context loading,
# auto push + PR).
#
# Credentials are NOT configured here:
#   - AWS Bedrock comes from the standard AWS credential chain (env / profile / role).
#   - GitHub uses the GITHUB_TOKEN (or GH_TOKEN) environment variable.

variable "anthropic_api_key" {
  type = string
}

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
    provider   = "bedrock"
    aws_region = "us-east-1"
    # model_id defaults to a Claude Sonnet inference profile on Bedrock.
  }
}

# One agent with all of the plugin's tools.
agent "engineer" {
  model       = models.anthropic.claude_sonnet_4
  personality = "A pragmatic senior engineer who ships small, well-tested changes and reviews thoroughly."
  tools       = [plugins.localdev.all]
}
