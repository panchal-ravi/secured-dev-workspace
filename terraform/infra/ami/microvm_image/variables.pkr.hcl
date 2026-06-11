variable "owner" {
  type        = string
  description = "Owner tag/prefix; the secured-codespace module looks up this AMI by '<owner>-microvm-workspace-*'"
  default     = "rp"
}

variable "aws_region" {
  type        = string
  description = "AWS region for the build"
  default     = "ap-southeast-1"
}

variable "aws_instance_type" {
  type        = string
  description = "Build instance type. MUST be bare metal (e.g. c5.metal) — /dev/kvm must be present so the `kata-runtime check` + `docker run --runtime=kata` gates can validate hardware virtualization. Nitro guests (t3/g4dn-non-metal) have no nested KVM."
  default     = "c5.metal"
}

variable "nomad_version" {
  type        = string
  description = "Nomad Enterprise version baked in (must match the server node so the client can join)"
  default     = "1.11.6+ent"
}

variable "docker_version" {
  type        = string
  description = <<-EOT
    docker-ce (+cli) apt version to PIN. Docker 28/29 (containerd 2.x) breaks the
    Kata shim with `failed to create shim task: invalid namespace type`; Docker
    27.5.1 launches Kata microVMs correctly (verified on c5.metal). Pin until Kata
    is confirmed compatible with newer Docker. Find the exact string with
    `apt-cache madison docker-ce`.
  EOT
  default     = "5:27.5.1-1~ubuntu.24.04~noble"
}

variable "kata_version" {
  type        = string
  description = "Kata Containers release tag (no leading v) from github.com/kata-containers/kata-containers; the kata-static-<version>-amd64.tar.xz asset is installed to /opt/kata. Check the project for the latest stable release before building."
  default     = "3.10.1"
}

variable "cni_version" {
  type        = string
  description = "CNI plugins release tag (matches the base image)"
  default     = "v1.9.0"
}
