# ─────────────────────────────────────────────────────────────────────────────
# End-to-end Foundry IQ wiring:
#   1. azurerm_search_service       — the Search service (managed by azurerm)
#   2. azurefoundry_knowledge_source — the corpus
#   3. azurefoundry_knowledge_base   — bundles sources, exposes one MCP URL
#   4. azurerm_..._project_connection — RemoteTool / ProjectManagedIdentity
#   5. azurefoundry_agent_v2.tools.knowledge_base — the typed shorthand
# ─────────────────────────────────────────────────────────────────────────────

resource "azurefoundry_knowledge_base" "fraud" {
  name            = "fraud-policy-kb"
  search_endpoint = azurerm_search_service.this.endpoint

  description = "Fraud-policy KB."

  retrieval_instructions = "Use this KB for any question about fraud rules, thresholds, or merchant tiers."
  output_mode            = "answerSynthesis"

  knowledge_sources = [
    { name = azurefoundry_knowledge_source.fraud_policies.name },
  ]

  # AOAI deployment used for query planning when reasoning_effort is low/medium.
  # Prefer user-assigned managed identity over api_key — Search will mint
  # AOAI tokens against this identity at query time.
  models = [
    {
      azure_open_ai = {
        resource_uri              = azurerm_cognitive_account.aoai.endpoint
        deployment_id             = azurerm_cognitive_deployment.gpt4o_mini.name
        model_name                = "gpt-4o-mini"
        user_assigned_identity_id = azurerm_user_assigned_identity.search.id
      }
    }
  ]

  retrieval_reasoning_effort = {
    kind = "low"
  }
}

# RBAC: Search MI needs Cognitive Services User on the AOAI account so the
# KB can call gpt-4o-mini for query planning.
resource "azurerm_role_assignment" "search_to_aoai" {
  scope                = azurerm_cognitive_account.aoai.id
  role_definition_name = "Cognitive Services User"
  principal_id         = azurerm_search_service.this.identity[0].principal_id
}

# Project connection authorizing Foundry agents to call the KB MCP endpoint.
# Connection lives on the ARM plane — this provider does NOT manage it.
# Pulumi equivalent: azure-native:cognitiveservices:Connection.
resource "azurerm_cognitive_account_project_connection" "fraud_kb" {
  name                = "fraud-kb-mcp-conn"
  project_id          = azurerm_cognitive_account_project.this.id
  category            = "RemoteTool"
  authentication_type = "ProjectManagedIdentity"
  target              = azurefoundry_knowledge_base.fraud.mcp_endpoint
  is_shared_to_all    = true
  metadata = {
    ApiType  = "Azure"
    audience = "https://search.azure.com/"
  }
}

# RBAC: project MI needs Search Index Data Reader on the Search service so
# the KB MCP traffic can read indexes at query time.
resource "azurerm_role_assignment" "project_to_search" {
  scope                = azurerm_search_service.this.id
  role_definition_name = "Search Index Data Reader"
  principal_id         = azurerm_cognitive_account_project.this.identity[0].principal_id
}

# Agent attaches the KB via the typed knowledge_base tool variant. The provider
# expands this into an mcp wire entry with allowed_tools=["knowledge_base_retrieve"].
resource "azurefoundry_agent_v2" "triage" {
  name  = "fraud-triage"
  kind  = "prompt"
  model = "gpt-4o-mini"

  instructions = <<-EOT
    You are a fraud-triage assistant. Use the knowledge base tool to answer
    any question about fraud rules, thresholds, or merchant tiers. If the KB
    doesn't contain the answer, respond with "I don't know". Always cite the
    retrieved sources.
  EOT

  tools {
    type = "knowledge_base"
    knowledge_base = {
      knowledge_base_endpoint = azurefoundry_knowledge_base.fraud.mcp_endpoint
      project_connection_id   = azurerm_cognitive_account_project_connection.fraud_kb.name
    }
  }
}
