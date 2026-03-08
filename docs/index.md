---
page_title: "Provider: Azure AI Foundry"
description: |-
  The Azure AI Foundry provider manages agents, files, and vector stores in Azure AI Foundry projects.
---

# Azure AI Foundry Provider

The **azurefoundry** provider manages resources in the [Azure AI Foundry Agent Service](https://learn.microsoft.com/azure/ai-foundry/agents/).

Use this provider to create and manage AI agents, upload files, and create vector stores for knowledge retrieval — all as Terraform-managed infrastructure.

## Authentication

The provider supports four authentication methods, evaluated in this order:

1. **API Key** — simplest option, suitable for testing
2. **Service Principal** — recommended for CI/CD pipelines
3. **Azure CLI** — recommended for local development
4. **Default Azure Credential chain** — covers managed identity, workload identity, etc.

## Example Usage

### Azure CLI (local development)

```hcl
terraform {
  required_providers {
    azurefoundry = {
      source  = "andrewCluey/azurefoundry"
      version = "~> 0.1"
    }
  }
}

provider "azurefoundry" {
  project_endpoint = "https://<resource>.services.ai.azure.com/api/projects/<project>"
  use_azure_cli    = true
}
```

### Service Principal

```hcl
provider "azurefoundry" {
  project_endpoint = "https://<resource>.services.ai.azure.com/api/projects/<project>"
  tenant_id        = var.tenant_id
  client_id        = var.client_id
  client_secret    = var.client_secret
}
```

### API Key

```hcl
provider "azurefoundry" {
  project_endpoint = "https://<resource>.services.ai.azure.com/api/projects/<project>"
  api_key          = var.api_key
}
```

## Argument Reference

| Name | Required | Description |
|------|----------|-------------|
| `project_endpoint` | Yes | The Azure AI Foundry project endpoint. Can also be set via `AZURE_AI_FOUNDRY_PROJECT_ENDPOINT`. |
| `api_key` | No | API key for authentication. Can also be set via `AZURE_AI_FOUNDRY_API_KEY`. |
| `tenant_id` | No | Azure AD tenant ID. Can also be set via `AZURE_TENANT_ID`. |
| `client_id` | No | Service principal client ID. Can also be set via `AZURE_CLIENT_ID`. |
| `client_secret` | No | Service principal client secret. Can also be set via `AZURE_CLIENT_SECRET`. |
| `use_azure_cli` | No | Use credentials from `az login`. Defaults to `false`. |
