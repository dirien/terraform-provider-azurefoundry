# Prompt agent with file search over a vector store.
resource "azurefoundry_agent_v2" "researcher" {
  name         = "tf-researcher"
  kind         = "prompt"
  model        = "gpt-4o-mini"
  instructions = "You are a Terraform specialist. Cite file:line for every claim."

  tools {
    type             = "file_search"
    vector_store_ids = [azurefoundry_vector_store_v2.docs.id]
    max_num_results  = 20
  }

  tools {
    type = "code_interpreter"
    code_interpreter = {
      file_ids = [azurefoundry_file_v2.style_guide.id]
    }
  }

  metadata = {
    environment = "dev"
  }
}

# Hosted agent: ship a container that speaks the Foundry Responses protocol.
# The agent's managed identity is exposed via instance_identity for RBAC.
resource "azurefoundry_agent_v2" "fraud_detection" {
  name         = "fraud-detection"
  kind         = "hosted"
  model        = "gpt-4o-mini"
  image        = "myacr.azurecr.io/fraud-agent:0.1.0"
  cpu          = "1"
  memory       = "2Gi"
  instructions = "Triage incoming transactions; escalate when score > 0.85."

  container_protocol_versions = [
    {
      protocol = "responses"
      version  = "v1"
    }
  ]

  environment_variables = {
    LOG_LEVEL = "info"
  }

  warmup         = true
  warmup_timeout = "5m"
}

# Grant the hosted agent's managed identity Azure AI User on the Foundry account.
resource "azurerm_role_assignment" "fraud_agent_runtime" {
  scope                = data.azurerm_cognitive_account.foundry.id
  role_definition_name = "Azure AI User"
  principal_id         = azurefoundry_agent_v2.fraud_detection.instance_identity.principal_id
}
