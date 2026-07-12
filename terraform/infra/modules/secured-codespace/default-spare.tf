# ---------------------------------------------------------------------------
# TEMPORARY default-pool spare node — a single standard-CPU EC2 that joins the
# all-in-one node as a Nomad CLIENT in node_pool "default". Its sole purpose is
# to give the "default" pool a SECOND node so a workspace (pinned to that pool)
# can be forced to reschedule cross-node, proving the Nomad->Boundary host-address
# sync updates the target's host to the new node IP. Same-AZ (public[0]) as the
# all-in-one node so the workspace's EBS /home/dev volume reattaches; reuses the
# agent AMI (Docker + CNI + Nomad client) — the system EBS CSI node plugin lands
# on it automatically. Gated by var.enable_default_spare (default false); destroy
# it (flag back to false + apply) once the cross-node test passes.
# ---------------------------------------------------------------------------

data "aws_ami" "default_spare" {
  count = var.enable_default_spare ? 1 : 0

  most_recent = true
  owners      = ["self"]

  filter {
    name   = "name"
    values = ["${var.owner}-agent-node-*"]
  }
}

data "cloudinit_config" "default_spare" {
  count = var.enable_default_spare ? 1 : 0

  gzip          = true
  base64_encode = true

  part {
    filename     = "bootstrap-default-spare.sh"
    content_type = "text/x-shellscript"
    content = templatefile("${path.module}/templates/bootstrap-agents.sh.tftpl", {
      nomad_config = templatefile("${path.module}/templates/nomad-default-spare.hcl.tftpl", {
        main_private_ip = aws_instance.this.private_ip
      })
      nomad_service = file("${path.root}/config/nomad.service")
      nomad_crt     = tls_self_signed_cert.nomad.cert_pem
      nomad_key     = tls_private_key.nomad.private_key_pem
      nomad_license = var.nomad_license
    })
  }
}

resource "aws_instance" "default_spare" {
  count = var.enable_default_spare ? 1 : 0

  ami                         = data.aws_ami.default_spare[0].id
  instance_type               = var.agent_instance_type
  key_name                    = aws_key_pair.this.key_name
  subnet_id                   = aws_subnet.public[0].id
  vpc_security_group_ids      = [aws_security_group.instance.id]
  associate_public_ip_address = true
  user_data_base64            = data.cloudinit_config.default_spare[0].rendered
  iam_instance_profile        = aws_iam_instance_profile.instance.name

  root_block_device {
    volume_size = var.agent_root_volume_size
  }

  tags = {
    Name  = "${local.name}-default-spare"
    owner = var.owner
  }

  depends_on = [
    aws_instance.this,
    aws_route_table_association.public,
  ]

  provisioner "remote-exec" {
    inline = [
      "echo 'Waiting for default-spare node bootstrap to finish...'",
      "timeout 900 bash -c 'until [ -f /home/ubuntu/.agent-ready ]; do sleep 10; done' || { echo 'default-spare bootstrap did not complete'; sudo tail -n 100 /var/log/agent-bootstrap.log; exit 1; }",
      "echo 'default-spare node ready.'",
    ]

    connection {
      host        = self.public_ip
      user        = "ubuntu"
      agent       = false
      private_key = tls_private_key.ssh.private_key_openssh
    }
  }
}

output "default_spare_private_ip" {
  description = "Private IP of the temporary default-pool spare node, or \"\" when disabled."
  value       = var.enable_default_spare ? aws_instance.default_spare[0].private_ip : ""
}
