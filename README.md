# terraform-provider-azurefoundry

Terraform / OpenTofu provider for Azure AI Foundry. Manages agents, files, vector stores, memory stores, toolboxes, knowledge sources, knowledge bases, and project-level index registrations across the Foundry data plane and the Azure AI Search data plane.

> Forked from [andrewCluey/terraform-provider-azurefoundry](https://github.com/andrewCluey/terraform-provider-azurefoundry) and diverged from there. The original covered the classic Assistants API; v0.7.x added Foundry's v2 hosted-agent surface, v0.8.x added Foundry IQ (knowledge bases / knowledge sources) plus toolboxes. Latest tag: **v0.8.4**.

## Install

```hcl
terraform {
  required_providers {
    azurefoundry = {
      source  = "dirien/azurefoundry"
      version = "~> 0.8.4"
    }
  }
}

provider "azurefoundry" {
  project_endpoint = "https://<account>.services.ai.azure.com/api/projects/<project>"
  use_azure_cli    = true
}
```

Published on the Terraform Registry — `terraform init` pulls it.

## Authentication

The provider tries credential methods in this order. First one with all required inputs configured wins.

1. **API key** — `api_key` attribute or `AZURE_AI_FOUNDRY_API_KEY` env var. Fast path for local dev. Note: Search-data-plane resources (knowledge sources, knowledge bases) require Entra auth and will reject API-key mode with a clear error.
2. **OIDC client assertion** — `tenant_id` + `client_id` + `oidc_token` (also reads `AZURE_OIDC_TOKEN` / `ARM_OIDC_TOKEN`). What Pulumi ESC and federated GitHub Actions use.
3. **Service principal secret** — `tenant_id` + `client_id` + `client_secret`.
4. **Azure CLI** — `use_azure_cli = true` after `az login`.
5. **Default Azure credential chain** — managed identity, workload identity, environment variables. The fallback when nothing else is configured.

All credential attributes are marked sensitive, so they don't print in plan output.

## Resources

The v1 / v2 split mirrors Foundry's own. v1 talks to the classic Assistants API and stays for resources that haven't been ported yet. v2 talks to the newer `/agents` surface and is the right choice for anything new.

| Resource | Surface | Notes |
|---|---|---|
| `azurefoundry_agent` | Foundry v1 (Assistants) | Classic agents. One tool block per type. |
| `azurefoundry_agent_v2` | Foundry v2 | Prompt agents and hosted (container) agents. Polymorphic `tools` block with 10 variants, including the typed `knowledge_base` shorthand that expands to an MCP tool with the right defaults. |
| `azurefoundry_file` | Foundry v1 | File CRUD for the classic API. |
| `azurefoundry_file_v2` | Foundry v2 | Same wire shape today; reserved for v2-specific features as they land. |
| `azurefoundry_vector_store` | Foundry v1 | Vector store CRUD. |
| `azurefoundry_vector_store_v2` | Foundry v2 | |
| `azurefoundry_memory_store_v2` | Foundry v2 (preview) | Foundry Memory Stores. |
| `azurefoundry_toolbox_v2` | Foundry v2 (preview) | Project-scoped, MCP-compatible bundle of tools. Computed `consumer_endpoint` is what agents wire into an `mcp` tool block. |
| `azurefoundry_knowledge_source` | Azure AI Search (preview) | Foundry IQ corpus. `kind` ∈ {`azureBlob`, `searchIndex`}. |
| `azurefoundry_knowledge_base` | Azure AI Search (preview) | Bundles one or more knowledge sources behind one MCP endpoint. |
| `azurefoundry_project_index` | Foundry v1 | Registers an existing Search index in the project's Indexes catalog. Powers the Foundry IQ → Indexes tab. |

### Two data planes, one provider

Most resources hit the Foundry data plane (`*.services.ai.azure.com/api/projects/...`) with the AAD scope `https://ai.azure.com/.default`. Knowledge sources and knowledge bases hit the Azure AI Search data plane (`*.search.windows.net/...`) with `https://search.azure.com/.default` and require Entra credentials. The provider mints both token types from the same configured credential — you don't have to set anything up twice.

### What the provider doesn't manage

By design, this provider sticks to the data-plane surfaces above. ARM-level objects belong with the upstream Azure providers:

- **The Foundry account / project itself** — `azurerm_cognitive_account` + `azurerm_cognitive_account_project`, or `pulumi-azure-native:cognitiveservices:Account` / `Project`.
- **Project connections** (`RemoteTool`, `CognitiveSearch`, etc.) — `azurerm_cognitive_account_project_connection` or `pulumi-azure-native:cognitiveservices:Connection`.
- **The Azure AI Search service** — `azurerm_search_service` / `pulumi-azure-native:search:Service`.
- **The contents and schema of an index** — `azurerm_search_index`.

Where the wiring matters (KB → project connection, project_index → Search service), the per-resource example HCL under `examples/resources/<name>/resource.tf` shows the full upstream chain.

## Worked example

End-to-end Foundry IQ retrieval — knowledge source, knowledge base, agent attach. The `azurerm_*` setup (Search service, AOAI deployment, project connection, RBAC) is omitted here; see `examples/resources/azurefoundry_knowledge_base/resource.tf` for the unabridged version.

```hcl
resource "azurefoundry_knowledge_source" "fraud_policies" {
  name            = "fraud-policies-ks"
  search_endpoint = azurerm_search_service.this.endpoint
  kind            = "azureBlob"

  azure_blob = {
    connection_string = "ResourceId=${azurerm_storage_account.policies.id}"
    container_name    = "fraud-policies"
  }
}

resource "azurefoundry_knowledge_base" "fraud" {
  name            = "fraud-policy-kb"
  search_endpoint = azurerm_search_service.this.endpoint

  knowledge_sources = [{ name = azurefoundry_knowledge_source.fraud_policies.name }]

  models = [{
    azure_open_ai = {
      resource_uri              = azurerm_cognitive_account.aoai.endpoint
      deployment_id             = "gpt-4o-mini"
      user_assigned_identity_id = azurerm_user_assigned_identity.search.id
    }
  }]

  retrieval_reasoning_effort = { kind = "low" }
}

resource "azurefoundry_agent_v2" "triage" {
  name         = "fraud-triage"
  kind         = "prompt"
  model        = "gpt-4o-mini"
  instructions = "Use the knowledge base to answer fraud-policy questions. Cite the retrieved source."

  tools {
    type = "knowledge_base"
    knowledge_base = {
      knowledge_base_endpoint = azurefoundry_knowledge_base.fraud.mcp_endpoint
      project_connection_id   = azurerm_cognitive_account_project_connection.fraud_kb.name
    }
  }
}
```

Per-resource argument reference and attribute schemas live on the registry: [registry.terraform.io/providers/dirien/azurefoundry](https://registry.terraform.io/providers/dirien/azurefoundry/latest/docs).

## Building from source

For local development against an unreleased build:

```bash
go install .
```

Then tell Terraform to use that binary via a dev-override in `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "dirien/azurefoundry" = "/Users/you/go/bin"
  }
  direct {}
}
```

Replace the path with whatever `go env GOBIN` returns (or `$(go env GOPATH)/bin` if `GOBIN` is empty). With a dev-override active you skip `terraform init` — Terraform will warn if you try to use the override in a workspace whose `.terraform.lock.hcl` doesn't already include the provider, which is normal.

## Contributing

Issues and PRs at https://github.com/dirien/terraform-provider-azurefoundry. The repo follows the root [`AGENTS.md`](AGENTS.md) plus per-directory `AGENTS.md` files under `internal/`. CI runs `go build ./...`, `golangci-lint run ./...` (40+ linters, zero `nolint:` tolerance), and `tfplugindocs generate` drift detection — a PR doesn't merge until all three pass.

## License

MPL-2.0. See [`LICENSE`](LICENSE).
