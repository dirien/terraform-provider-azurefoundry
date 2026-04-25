resource "azurefoundry_memory_store_v2" "fraud_ops" {
  name            = "fraud_ops_memory"
  description     = "Long-term memory for the fraud-ops agent suite"
  chat_model      = "gpt-4o-mini"
  embedding_model = "text-embedding-3-small"

  user_profile_enabled = true
  chat_summary_enabled = true
  user_profile_details = "Avoid sensitive data: age, financials, precise location, credentials."
}

# Attach the memory store to an agent via the memory_search tool.
resource "azurefoundry_agent_v2" "fraud_detection" {
  name  = "fraud-detection-agent"
  kind  = "prompt"
  model = "gpt-4o-mini"

  tools {
    type = "memory_search"
    memory_search = {
      memory_store_name = azurefoundry_memory_store_v2.fraud_ops.name
      scope             = "{{$userId}}"
      update_delay      = 300
    }
  }
}
