---
page_title: "azurefoundry_memory_store_v2 Resource - azurefoundry"
subcategory: ""
description: |-
  Manages an Azure AI Foundry Memory Store (preview).
  A memory store is the long-term memory backend for Foundry agents. Attach
  it to an agent via the `memory_search` tool on `azurefoundry_agent_v2`.
---

# azurefoundry_memory_store_v2 (Resource)

Manages an Azure AI Foundry Memory Store (preview).

~> **Preview** The memory store API uses `api-version=2025-11-15-preview` and the
shape may change before GA.

## Example Usage

```terraform
resource "azurefoundry_memory_store_v2" "fraud_ops" {
  name             = "fraud_ops_memory"
  description      = "Long-term memory for the fraud-ops agent suite"
  chat_model       = "gpt-4o-mini"
  embedding_model  = "text-embedding-3-small"

  user_profile_enabled = true
  chat_summary_enabled = true
  user_profile_details = "Avoid sensitive data: age, financials, precise location, credentials."
}

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
```

## Schema

### Required

- `name` (String) — name of the memory store, unique within the project.
- `chat_model` (String) — deployment name of the chat model used to extract and consolidate memories.
- `embedding_model` (String) — deployment name of the embedding model used for retrieval.

### Optional

- `description` (String) — human-readable description, visible in the Foundry portal.
- `user_profile_enabled` (Boolean) — extract user-profile memories. Defaults to `false`.
- `chat_summary_enabled` (Boolean) — extract chat-summary memories. Defaults to `false`.
- `user_profile_details` (String) — free-form guidance about what user-profile data to keep or avoid.
- `metadata` (Map of String) — up to 16 key/value string pairs.

### Read-Only

- `id` (String) — memory store ID assigned by the Foundry service.
- `created_at` (Number) — Unix timestamp when the memory store was created.
