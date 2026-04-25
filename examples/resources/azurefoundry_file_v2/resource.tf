resource "azurefoundry_file_v2" "style_guide" {
  source = "${path.module}/terraform-style-guide.md"
}

# Multiple files can be uploaded and indexed together in a vector store.
resource "azurefoundry_file_v2" "refactor_skill" {
  source = "${path.module}/terraform-refactor-module.md"
}
