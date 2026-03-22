---
page_title: "azurefoundry_file Resource - azurefoundry"
description: |-
  Uploads a local file to an Azure AI Foundry project.
---

# azurefoundry_file

Uploads a local file to an Azure AI Foundry project. The resulting file ID can be used with `azurefoundry_vector_store` for file search, or passed directly to `code_interpreter_file_ids` on an `azurefoundry_agent`.

~> **Note** Files are immutable in the Foundry API. Changing `source` will delete the old file and upload a new one.

## Example Usage

```hcl
resource "azurefoundry_file" "knowledge" {
  source = "./docs/knowledge.pdf"
}

output "file_id" {
  value = azurefoundry_file.knowledge.id
}
```

## Argument Reference

### Required

- `source` (String) - Path to the local file to upload. Changing this forces a new file to be uploaded.

### Optional

- `purpose` (String) - Intended use of the file. Currently only `assistants` is supported. Defaults to `assistants`.

## Attributes Reference

- `id` (String) - The file ID assigned by the Foundry service.
- `created_at` (Number) - Unix timestamp of when the file was uploaded.
- `bytes` (Number) - Size of the uploaded file in bytes.
- `filename` (String) - The filename as stored in the Foundry service.