resource "azurefoundry_agent" "support_assistant" {
  name         = "support-assistant"
  description  = "Tier-1 customer support agent"
  model        = "gpt-4o"
  instructions = "You are a helpful customer support assistant. Cite ticket numbers when summarizing."
  temperature  = 0.7

  tools {
    type = "code_interpreter"
  }

  tools {
    type = "file_search"
  }

  metadata = {
    environment = "production"
    owner       = "support-team"
  }
}
