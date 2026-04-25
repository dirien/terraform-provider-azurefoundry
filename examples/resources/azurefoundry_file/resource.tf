resource "azurefoundry_file" "knowledge_base" {
  source = "${path.module}/knowledge-base.md"
}

# The resulting file ID can be used in a vector store or attached directly
# to an agent's code_interpreter tool.
output "file_id" {
  value = azurefoundry_file.knowledge_base.id
}
