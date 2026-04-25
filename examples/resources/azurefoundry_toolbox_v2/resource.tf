# ─────────────────────────────────────────────────────────────────────────────
# Project connection for an authenticated MCP server (lives on the ARM plane,
# managed by the upstream azurerm provider — NOT by this provider).
#
# Pulumi equivalent: azure-native:cognitiveservices:Connection.
# Reference its `name` as `project_connection_id` on tool blocks below.
# ─────────────────────────────────────────────────────────────────────────────
resource "azurerm_cognitive_account_project_connection" "aurora_risk_mcp" {
  name              = "aurora-risk-mcp-conn"
  project_id        = azurerm_cognitive_account_project.this.id
  category          = "RemoteTool"
  target            = "https://mcp-risk.example.com/mcp"
  authentication_type = "CustomKeys"

  custom_keys = {
    "Authorization" = "Bearer ${var.aurora_risk_token}"
  }
}

# ─────────────────────────────────────────────────────────────────────────────
# Project-scoped Foundry Toolbox bundling MCP + Web Search + Azure AI Search.
# Use one toolbox across many agents — the consumer endpoint exposes them all
# behind a single MCP-compatible URL, and the portal Tools view picks them up
# automatically (Build → Tools → Toolboxes).
# ─────────────────────────────────────────────────────────────────────────────
resource "azurefoundry_toolbox_v2" "fraud_ops" {
  name        = "fraud-ops"
  description = "Curated tools for fraud-triage agents."

  # Authenticated MCP server — references the RemoteTool connection above.
  # project_connection_id takes the connection's `name`, not its full ARM ID.
  tools {
    type = "mcp"
    mcp = {
      server_label          = "aurora-risk"
      server_url            = "https://mcp-risk.example.com/mcp"
      require_approval      = "never"
      project_connection_id = azurerm_cognitive_account_project_connection.aurora_risk_mcp.name
    }
  }

  # Built-in web search — no connection required.
  tools {
    type = "web_search"
  }

  # Azure AI Search over an existing index. The index's connection is also
  # a RemoteTool-or-AzureAISearch project connection managed by azurerm.
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

# Canary toolbox: post a new version but DON'T promote it. Validate against
# versioned_endpoint with an MCP client first, then flip promote_default to
# true (or run a separate `pulumi up` / `terraform apply`) to go live.
resource "azurefoundry_toolbox_v2" "fraud_ops_canary" {
  name               = "fraud-ops-canary"
  description        = "Canary: validates new tool wiring before promotion."
  promote_default    = false
  prune_old_versions = false

  tools {
    type = "mcp"
    mcp = {
      server_label = "aurora-risk-next"
      server_url   = "https://mcp-risk-next.example.com/mcp"
    }
  }
}

# Agent that consumes the toolbox via its standard mcp tool block. Multiple
# agents can wire to the same consumer_endpoint — that's the reuse benefit.
# Note: agent runtimes that call the endpoint must set the
# `Foundry-Features: Toolboxes=V1Preview` header themselves; this provider
# only sets it on its own management calls.
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
