# Project-scoped Foundry Toolbox bundling MCP + Web Search + Azure AI Search.
# Use one toolbox across many agents — the consumer endpoint exposes them all
# behind a single MCP-compatible URL, and the portal Tools view picks them up
# automatically (Build → Tools → Toolboxes).
resource "azurefoundry_toolbox_v2" "fraud_ops" {
  name        = "fraud-ops"
  description = "Curated tools for fraud-triage agents."

  # MCP server fronted by a project connection (auth managed centrally).
  # Create the connection itself via azurerm_cognitive_account_project_connection
  # or pulumi-azure-native's CognitiveServices Connection — those manage the
  # ARM resource. Reference its name here.
  tools {
    type = "mcp"
    mcp = {
      server_label          = "aurora-risk"
      server_url            = "https://mcp-risk.example.com/mcp"
      require_approval      = "never"
      project_connection_id = "aurora-risk-mcp-conn"
    }
  }

  tools {
    type = "web_search"
  }

  tools {
    type = "azure_ai_search"
    azure_ai_search = {
      indexes = [
        {
          project_connection_id = "fraud-aisearch-conn"
          index_name            = "fraud-cases"
          query_type            = "vector_semantic_hybrid"
          top_k                 = 5
        }
      ]
    }
  }
}

# Promote a new version only after smoke-testing it against versioned_endpoint.
# In a typical workflow you'd flip promote_default = true after validation.
resource "azurefoundry_toolbox_v2" "fraud_ops_canary" {
  name             = "fraud-ops-canary"
  description      = "Canary: validates new tool wiring before promotion."
  promote_default  = false # post the version, do not flip default
  prune_old_versions = false

  tools {
    type = "mcp"
    mcp = {
      server_label = "aurora-risk-next"
      server_url   = "https://mcp-risk-next.example.com/mcp"
    }
  }
}

# An agent that consumes the toolbox via its standard mcp tool block.
# Set Foundry-Features: Toolboxes=V1Preview on the runtime client when you
# call the agent — Foundry rejects toolbox MCP traffic without it.
resource "azurefoundry_agent_v2" "triage" {
  name         = "fraud-triage"
  kind         = "prompt"
  model        = "gpt-4o-mini"
  instructions = "Triage incoming transactions using the fraud-ops toolbox."

  tools {
    type = "mcp"
    mcp = {
      server_label     = "fraud-ops"
      server_url       = azurefoundry_toolbox_v2.fraud_ops.consumer_endpoint
      require_approval = "never"
    }
  }
}
