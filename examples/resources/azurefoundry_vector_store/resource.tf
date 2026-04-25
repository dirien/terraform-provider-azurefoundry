resource "azurefoundry_vector_store" "knowledge" {
  name     = "support-knowledge-base"
  file_ids = [azurefoundry_file.knowledge_base.id]

  # Auto-expire after 7 days of inactivity.
  expiry_anchor = "last_active_at"
  expiry_days   = 7

  metadata = {
    environment = "production"
  }
}
