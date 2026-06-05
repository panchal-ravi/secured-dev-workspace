variable "owner" {
  type        = string
  description = "Owner tag/prefix; the secured-codespace module looks up this AMI by '<owner>-gpu-workspace-*'"
  default     = "rp"
}

variable "aws_region" {
  type        = string
  description = "AWS region for the build"
  default     = "ap-southeast-1"
}

variable "aws_instance_type" {
  type        = string
  description = "Build instance type. MUST be a GPU type (a T4 must be present so the reboot + nvidia-smi gate can validate the driver loaded)."
  default     = "g4dn.xlarge"
}

variable "nomad_version" {
  type        = string
  description = "Nomad Enterprise version baked in (must match the server node so the client can join)"
  default     = "1.11.6+ent"
}

variable "nvidia_driver_package" {
  type        = string
  description = "APT package for the NVIDIA datacenter driver. The -server variant is the recommended branch for the Tesla T4. 550-server apt-resolves to driver 580 (CUDA 13 capable), which forward-runs the gpu-workspace image's CUDA 12.6 base."
  default     = "nvidia-driver-550-server"
}

variable "nomad_device_nvidia_version" {
  type        = string
  description = "nomad-device-nvidia external device plugin release (no leading v) from releases.hashicorp.com, installed to /opt/nomad/plugins. Latest stable is 1.1.0."
  default     = "1.1.0"
}

variable "cni_version" {
  type        = string
  description = "CNI plugins release tag (matches the base image)"
  default     = "v1.9.0"
}
