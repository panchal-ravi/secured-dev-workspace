# ---------------------------------------------------------------------------
# AWS EBS CSI driver on Nomad (controller + per-node), in the "infra" namespace.
# Gives workspaces a durable per-workspace EBS volume for /home/dev that survives
# node crash / instance replacement (replacing the node-local mkdir host volume).
# The portal provisions/deletes the volumes at workspace create/destroy via the
# Nomad CSI API (internal/hashistack/nomad.go). Only WORKSPACE /home/dev volumes
# use CSI; the mkdir stores (portal/LiteLLM Postgres, MCP SQLite) are unchanged.
#
# EC2 access is via the node IAM instance profile (modules/secured-codespace/
# iam.tf) — no static credentials. The controller runs on the all-in-one node;
# the node plugin runs on every client (system job) and is privileged.
# ---------------------------------------------------------------------------
resource "nomad_job" "ebs_csi_controller" {
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/ebs-csi-controller.nomad.hcl.tftpl", {
    namespace  = nomad_namespace.infra.name
    image      = var.ebs_csi_driver_image
    aws_region = var.region
  })

  depends_on = [nomad_namespace.infra]
}

resource "nomad_job" "ebs_csi_node" {
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/ebs-csi-node.nomad.hcl.tftpl", {
    namespace  = nomad_namespace.infra.name
    image      = var.ebs_csi_driver_image
    aws_region = var.region
  })

  depends_on = [nomad_namespace.infra]
}
