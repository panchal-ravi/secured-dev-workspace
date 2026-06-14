variable "owner" {
  description = "Owner tag/prefix applied to resources and used to look up the AMI"
  type        = string
  default     = "rp"
}

variable "region" {
  description = "AWS region"
  type        = string
  default     = "ap-southeast-1"
}

variable "instance_type" {
  description = "EC2 instance type for the all-in-one Boundary node"
  type        = string
  default     = "t3.large"
}

variable "vpc_cidr" {
  description = "CIDR for the dedicated VPC"
  type        = string
  default     = "10.220.0.0/16"
}

variable "public_subnet_cidrs" {
  description = "Two public subnet CIDRs across two AZs (NLB requires the targets in the LB subnets)"
  type        = list(string)
  default     = ["10.220.10.0/24", "10.220.11.0/24"]
}

variable "root_volume_size" {
  description = "Root EBS volume size (GiB)"
  type        = number
  default     = 40
}

variable "enable_gpu_node" {
  description = "Provision the GPU Nomad client EC2 (NVIDIA T4). Off by default — the g4dn instance is costly, so opt in only when GPU workspaces are needed."
  type        = bool
  default     = false
}

variable "gpu_instance_type" {
  description = "EC2 instance type for the GPU Nomad client node (NVIDIA T4)"
  type        = string
  default     = "g4dn.xlarge"
}

variable "gpu_root_volume_size" {
  description = "Root EBS volume size (GiB) for the GPU node — large enough for CUDA images"
  type        = number
  default     = 60
}

variable "enable_agent_nodes" {
  description = "Provision the standard-CPU agent worker EC2s (Nomad clients in node pool \"agents\"). Off by default — opt in only when the agent-platform tier needs to schedule agent instances + wrapped MCP servers off the all-in-one node."
  type        = bool
  default     = false
}

variable "agent_node_count" {
  description = "Number of agent worker nodes to provision when enable_agent_nodes = true."
  type        = number
  default     = 1
}

variable "agent_instance_type" {
  description = "EC2 instance type for each agent worker node (standard CPU)."
  type        = string
  default     = "t3.large"
}

variable "agent_root_volume_size" {
  description = "Root EBS volume size (GiB) for each agent worker node."
  type        = number
  default     = 40
}

variable "enable_microvm_node" {
  description = "Provision the Kata microVM Nomad client EC2 (bare metal). Off by default — metal instances are costly, so opt in only when hardware-isolated microVM workspaces are needed."
  type        = bool
  default     = false
}

variable "microvm_instance_type" {
  description = "EC2 instance type for the microVM Nomad client node. MUST be bare metal (e.g. c5.metal) — Kata needs /dev/kvm, which Nitro guests do not expose."
  type        = string
  default     = "c5.metal"
}

variable "microvm_root_volume_size" {
  description = "Root EBS volume size (GiB) for the microVM node — room for the Kata guest kernel/rootfs + images"
  type        = number
  default     = 60
}

variable "boundary_version" {
  description = "Boundary Enterprise version baked into the AMI (informational; must end with +ent)"
  type        = string
  default     = "0.21.3+ent"

  validation {
    condition     = endswith(var.boundary_version, "+ent")
    error_message = "boundary_version must be an Enterprise build ending in '+ent'."
  }
}

variable "boundary_license" {
  description = "Boundary Enterprise license contents (read from a file by the caller)"
  type        = string
  sensitive   = true
}

variable "nomad_version" {
  description = "Nomad Enterprise version baked into the AMI (informational; must end with +ent)"
  type        = string
  default     = "1.11.6+ent"

  validation {
    condition     = endswith(var.nomad_version, "+ent")
    error_message = "nomad_version must be an Enterprise build ending in '+ent'."
  }
}

variable "nomad_license" {
  description = "Nomad Enterprise license contents (read from a file by the caller)"
  type        = string
  sensitive   = true
}

variable "vault_version" {
  description = "Vault Enterprise version baked into the AMI (informational; must end with +ent)"
  type        = string
  default     = "1.20.4+ent"

  validation {
    condition     = endswith(var.vault_version, "+ent")
    error_message = "vault_version must be an Enterprise build ending in '+ent'."
  }
}

variable "vault_license" {
  description = "Vault Enterprise license contents (read from a file by the caller)"
  type        = string
  sensitive   = true
}

variable "boundary_admin_login_name" {
  description = "Login name for the initial Boundary admin account"
  type        = string
  default     = "admin"
}

variable "boundary_admin_password" {
  description = "Password for the initial Boundary admin account (min 8 chars; avoid single quotes)"
  type        = string
  default     = "Password123!"
  sensitive   = true

  validation {
    condition     = length(var.boundary_admin_password) >= 8 && !strcontains(var.boundary_admin_password, "'")
    error_message = "boundary_admin_password must be at least 8 characters and must not contain a single quote."
  }
}

variable "boundary_org_name" {
  description = "Name of the org scope created under global"
  type        = string
  default     = "primary-org"
}
