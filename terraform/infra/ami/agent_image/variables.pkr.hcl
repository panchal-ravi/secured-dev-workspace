variable "owner" {
  type        = string
  description = "Owner tag/prefix; the secured-codespace module looks up this AMI by '<owner>-agent-node-*'"
  default     = "rp"
}

variable "aws_region" {
  type        = string
  description = "AWS region for the build"
  default     = "ap-southeast-1"
}

variable "aws_instance_type" {
  type        = string
  description = "Build instance type. Any standard CPU type works (no GPU/metal needed)."
  default     = "t3.large"
}

variable "nomad_version" {
  type        = string
  description = "Nomad Enterprise version baked in (must match the server node so the client can join)"
  default     = "1.11.6+ent"
}

variable "cni_version" {
  type        = string
  description = "CNI plugins release tag (matches the base/gpu image)"
  default     = "v1.9.0"
}
