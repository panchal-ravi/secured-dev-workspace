# ---------------------------------------------------------------------------
# Register the external GitHub secrets plugin (martinbaillie/vault-plugin-secrets-github)
# into Vault's plugin catalog. The binary is baked into the AMI at
# /opt/vault/plugins (see ami/base_image) and the directory is declared as
# plugin_directory in config/vault.hcl. Registration only records the binary in
# the catalog; per-project MOUNTS of it (github/<project>) are created in the
# project tier.
#
# The plugin mints short-lived GitHub App installation tokens, used as the
# dev-workspace git push credential — no static PAT anywhere.
#
# The sha256 here MUST equal the sha256 the AMI verified the download against
# (ami/base_image/variables.pkr.hcl github_plugin_sha256). Keep them in lockstep.
# ---------------------------------------------------------------------------

variable "github_plugin_version" {
  description = "vault-plugin-secrets-github release version (no leading v). Must match the AMI's baked binary."
  type        = string
  default     = "2.3.2"
}

variable "github_plugin_sha256" {
  description = "SHA-256 of the baked linux-amd64 plugin binary. MUST match ami/base_image github_plugin_sha256."
  type        = string
  default     = "72cb1f2775ee2abf12ffb725e469d0377fe7bbb93cd7aaa6921c141eddecab87"
}

# The catalog read returns `sha256` (different shape from the write's `sha256`
# param) which would show perpetual drift, so disable_read. Deregisters on destroy.
resource "vault_generic_endpoint" "github_plugin" {
  path                 = "sys/plugins/catalog/secret/vault-plugin-secrets-github"
  ignore_absent_fields = true
  disable_read         = true

  data_json = jsonencode({
    command = "vault-plugin-secrets-github"
    sha256  = var.github_plugin_sha256
    version = "v${var.github_plugin_version}"
  })
}
