# KV-v2 mount for project artifacts. The project-onboarding tier writes each
# project's allowed Nomad job templates under secret/projects/<project>/job-templates/<name>;
# the developer tier reads the selected template back at workspace-create time.
# Enabled once at the platform tier (uses the root vault provider in providers.tf).
resource "vault_mount" "kv" {
  path        = "secret"
  type        = "kv"
  options     = { version = "2" }
  description = "Project artifacts (Nomad job templates) — KV v2"
}
