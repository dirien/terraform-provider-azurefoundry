terraform {
  required_providers {
    azurefoundry = {
      source = "dirien/azurefoundry"
    }
  }
}

# Azure CLI auth is the simplest local-development setup. Pick one of the
# four supported auth methods — see the provider page for the full list.
provider "azurefoundry" {
  project_endpoint = "https://<resource>.services.ai.azure.com/api/projects/<project>"
  use_azure_cli    = true
}
