# ─────────────────────────────────────────────────────────────────────────────
# Register an existing Azure AI Search index with the Foundry project's
# Indexes catalog so it shows up under Foundry IQ → Indexes and is
# attachable from the legacy `azure_ai_search` agent tool variant.
#
# Three pieces have to exist before the project index registration:
#   1. The Search service itself (azurerm_search_service).
#   2. A populated index on it (typically managed out-of-band — the
#      provider does not create indexes).
#   3. A CognitiveSearch project connection pointing at the Search
#      service. Connections live on the ARM management plane — manage
#      them via azurerm or pulumi-azure-native.
# ─────────────────────────────────────────────────────────────────────────────

resource "azurerm_cognitive_account_project_connection" "search" {
  name                = "fraud-search-conn"
  project_id          = azurerm_cognitive_account_project.this.id
  category            = "CognitiveSearch"
  authentication_type = "AAD"
  target              = azurerm_search_service.this.endpoint
  is_shared_to_all    = true
  metadata = {
    ApiType  = "Azure"
    Type     = "azure_ai_search"
    Location = azurerm_search_service.this.location
  }
}

# RBAC on the Search service for the project's managed identity.
resource "azurerm_role_assignment" "project_search_data_contributor" {
  scope                = azurerm_search_service.this.id
  role_definition_name = "Search Index Data Contributor"
  principal_id         = azurerm_cognitive_account_project.this.identity[0].principal_id
}

resource "azurerm_role_assignment" "project_search_service_contributor" {
  scope                = azurerm_search_service.this.id
  role_definition_name = "Search Service Contributor"
  principal_id         = azurerm_cognitive_account_project.this.identity[0].principal_id
}

# Project index registration — what the portal Indexes tab actually lists.
resource "azurefoundry_project_index" "fraud_policies" {
  name        = "fraud-policies-index"
  kind        = "AzureSearch"
  description = "Fraud-policy index registered for the Foundry IQ Indexes tab."

  azure_search = {
    connection_name = azurerm_cognitive_account_project_connection.search.name
    index_name      = "fraud-policies-ks-index"

    # Optional: rename roles for citations / vector search. Skip the block
    # entirely to use the index's own field defaults.
    field_mapping = {
      content_fields  = ["chunk", "content"]
      title_field     = "title"
      url_field       = "source_url"
      vector_fields   = ["content_vector"]
      metadata_fields = ["category", "section"]
    }
  }

  tags = {
    domain = "fraud"
  }
}

# Attach the registered index to an agent via the existing
# azure_ai_search tool variant. Same connection_name + index_name as the
# project index registration so the portal traces all line up.
resource "azurefoundry_agent_v2" "policy_lookup" {
  name         = "policy-lookup"
  kind         = "prompt"
  model        = "gpt-4o-mini"
  instructions = "Look up fraud policy answers in the indexed corpus and cite the source field."

  tools {
    type = "azure_ai_search"
    azure_ai_search = {
      indexes = [
        {
          project_connection_id = azurerm_cognitive_account_project_connection.search.name
          index_name            = azurefoundry_project_index.fraud_policies.azure_search.index_name
          query_type            = "vector_semantic_hybrid"
          top_k                 = 5
        }
      ]
    }
  }
}
