# ---------------------------------------------------------------------------
# AWS EFS CSI driver on Nomad — MONOLITH (one job serving both controller + node),
# in the "infra" namespace. Backs per-project SHARED volumes: the portal calls the
# Nomad CSI API (internal/hashistack/nomad.go) to dynamically create one EFS ACCESS
# POINT per named shared volume against the filesystem provisioned in
# modules/secured-codespace/efs.tf.
#
# Unlike the EBS driver (split controller/node — see ebs-csi.tf), the EFS driver
# serves Identity + Controller + Node from a single binary, so Nomad runs it as a
# "monolith" on every client (system job). The controller half calls the EFS API
# (CreateAccessPoint / DeleteAccessPoint) via the node IAM instance profile
# (modules/secured-codespace/iam.tf) — no static credentials.
#
# Gated by var.enable_shared_volume (default off): no job when disabled.
# ---------------------------------------------------------------------------
resource "nomad_job" "efs_csi" {
  count            = var.enable_shared_volume ? 1 : 0
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/efs-csi.nomad.hcl.tftpl", {
    namespace  = nomad_namespace.infra.name
    image      = var.efs_csi_driver_image
    aws_region = var.region
  })

  depends_on = [nomad_namespace.infra]
}
