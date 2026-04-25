resource "azurefoundry_vector_store_v2" "docs" {
  name = "tf-skills"

  file_ids = [
    azurefoundry_file_v2.style_guide.id,
    azurefoundry_file_v2.refactor_skill.id,
  ]

  expiry_anchor = "last_active_at"
  expiry_days   = 7

  metadata = {
    environment = "dev"
  }
}
