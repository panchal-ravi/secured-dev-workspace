# ---------------------------------------------------------------------------
# AWS EFS for per-project SHARED volumes (package/build caches + datasets) that
# mount into workspaces ALONGSIDE their per-workspace EBS /home/dev — additive,
# nothing about the home volume changes. One elastic, encrypted filesystem; the
# EFS CSI driver (terraform/infra/efs-csi.tf) dynamically carves ONE access point
# per named shared volume, and the portal provisions/deletes those volumes via
# the Nomad CSI API (internal/hashistack/nomad.go). Each access point's enforced
# root path + POSIX id is the per-project tenant boundary — consistent with the
# platform's logical Vault/Nomad namespace-per-project isolation.
#
# Gated by var.enable_shared_volume (default off): when disabled, no EFS resources
# exist at all (zero diff vs the shipped deployment). NFS reaches the filesystem
# only from the node SG; EFS API access is via the node IAM instance profile
# (iam.tf), matching the EBS CSI pattern — no static credentials.
# ---------------------------------------------------------------------------
resource "aws_efs_file_system" "shared" {
  count = var.enable_shared_volume ? 1 : 0

  encrypted       = true
  throughput_mode = "elastic"

  tags = {
    Name  = "${local.name}-shared"
    owner = var.owner
  }
}

# One mount target per public subnet. Instances all launch in public[0] today, but
# provisioning a target per subnet keeps this correct if more subnets/AZs are added.
resource "aws_efs_mount_target" "shared" {
  count = var.enable_shared_volume ? length(aws_subnet.public) : 0

  file_system_id  = aws_efs_file_system.shared[0].id
  subnet_id       = aws_subnet.public[count.index].id
  security_groups = [aws_security_group.efs[0].id]
}

# NFS (2049) reachable ONLY from the node SG — no wider exposure. Stateful SG
# returns response traffic automatically, so no egress rule is needed on a mount
# target (it never initiates connections).
resource "aws_security_group" "efs" {
  count = var.enable_shared_volume ? 1 : 0

  name        = "${local.name}-efs"
  description = "EFS shared-volume NFS access from workspace nodes"
  vpc_id      = aws_vpc.main.id

  ingress {
    description     = "NFS from workspace nodes"
    from_port       = 2049
    to_port         = 2049
    protocol        = "tcp"
    security_groups = [aws_security_group.instance.id]
  }

  tags = {
    Name  = "${local.name}-efs"
    owner = var.owner
  }
}
