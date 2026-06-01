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
