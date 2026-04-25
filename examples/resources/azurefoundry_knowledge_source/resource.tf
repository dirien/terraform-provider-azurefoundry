# Knowledge source backed by an Azure Blob container. Search auto-generates
# the indexer pipeline (data source + skillset + indexer + index) on PUT.
resource "azurefoundry_knowledge_source" "fraud_policies" {
  name            = "fraud-policies-ks"
  search_endpoint = azurerm_search_service.this.endpoint
  kind            = "azureBlob"
  description     = "Fraud-policy markdown corpus."

  azure_blob = {
    # Either a key-based connection string or a managed-identity ResourceId
    # form ("ResourceId=/subscriptions/.../storageAccounts/<name>"). The latter
    # pairs with `ingestion_parameters_json.identity` below.
    connection_string = "ResourceId=${azurerm_storage_account.policies.id}"
    container_name    = "fraud-policies"

    # Search ingestion config — chunking, embedding model, schedule, identity.
    # Pass-through JSON keeps us compatible with preview shape changes.
    ingestion_parameters_json = jsonencode({
      identity = {
        "@odata.type"          = "#Microsoft.Azure.Search.DataUserAssignedIdentity"
        userAssignedIdentity = azurerm_user_assigned_identity.search.id
      }
      embeddingModel = {
        kind = "azureOpenAI"
        azureOpenAIParameters = {
          resourceUri  = azurerm_cognitive_account.aoai.endpoint
          deploymentId = azurerm_cognitive_deployment.embedding.name
          modelName    = "text-embedding-3-large"
        }
      }
      ingestionSchedule = {
        interval  = "P1D"
        startTime = "2026-04-25T00:00:00Z"
      }
    })
  }
}

# Knowledge source wrapping an existing Search index — useful when you've
# already populated the index out-of-band (e.g. via a separate indexer or
# Bulk Upload Action) and just want the Foundry IQ retrieval features on top.
resource "azurefoundry_knowledge_source" "support_kb_index" {
  name            = "support-kb-ks"
  search_endpoint = azurerm_search_service.this.endpoint
  kind            = "searchIndex"
  description     = "Support ticket / runbook index for grounding."

  search_index = {
    search_index_name           = "support-cases-v3"
    semantic_configuration_name = "default"
    search_fields = [
      { name = "title" },
      { name = "body" },
    ]
    source_data_fields = [
      { name = "id" },
      { name = "url" },
      { name = "score" },
    ]
  }
}
