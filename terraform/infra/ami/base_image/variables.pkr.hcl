variable "owner" {
  type        = string
  description = "Owner tag to which the artifacts belong"
  default     = "rp"
}

variable "aws_region" {
  type        = string
  description = "AWS Region for image"
  default     = "ap-southeast-1"
}

variable "aws_instance_type" {
  type        = string
  description = "Instance Type for Image"
  default     = "t2.small"
}

variable "boundary_version" {
  type        = string
  description = "Boundary Enterprise version to bake into the image"
  default     = "0.21.3+ent"
}

variable "consul_version" {
  type        = string
  description = "Consul Enterprise version to bake into the image"
  default     = "1.22.8+ent"
}

variable "nomad_version" {
  type        = string
  description = "Nomad Enterprise version to bake into the image"
  default     = "1.11.6+ent"
}

variable "vault_version" {
  type        = string
  description = "Vault Enterprise version to bake into the image"
  default     = "1.20.4+ent"
}

variable "github_plugin_version" {
  type        = string
  description = "martinbaillie/vault-plugin-secrets-github release version (no leading v) baked into /opt/vault/plugins. Mints short-lived GitHub App installation tokens."
  default     = "2.3.2"
}

variable "github_plugin_sha256" {
  type        = string
  description = "SHA-256 of the linux-amd64 plugin binary. MUST match the value Vault registers in its plugin catalog (terraform/infra). Pinned for supply-chain integrity."
  default     = "72cb1f2775ee2abf12ffb725e469d0377fe7bbb93cd7aaa6921c141eddecab87"
}

variable "envoy_version" {
  type        = string
  description = "Envoy version installed via func-e (service mesh proxy for Consul)"
}

variable "cni_version" {
  type        = string
  description = "CNI plugins release tag"
  default     = "v1.9.0"
}

variable "consul_cni_version" {
  type        = string
  description = "Consul-CNI plugin version"
  default     = "1.5.0"
}
