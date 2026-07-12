# ---------------------------------------------------------------------------
# GPU worker node — a second EC2 (NVIDIA T4 / g4dn.xlarge) that joins the
# all-in-one node as a Nomad CLIENT in node_pool "gpu". The portal schedules the
# gpu-workspace template here; the co-located Boundary worker on the main node
# dials this node's published SSH host ports over the VPC (the 2222-2399 SG rule
# in network.tf). No new Vault/Boundary/Postgres — those stay on the main node.
# ---------------------------------------------------------------------------

# AMI built by ami/gpu_image (Ubuntu 24.04 + NVIDIA driver + toolkit + Nomad +
# nomad-device-nvidia plugin).
data "aws_ami" "gpu" {
  count = var.enable_gpu_node ? 1 : 0

  most_recent = true
  owners      = ["self"]

  filter {
    name   = "name"
    values = ["${var.owner}-gpu-workspace-*"]
  }
}

locals {
  # Rendered with the all-in-one node's private IP so the client knows where to
  # retry_join the server and reach Vault.
  gpu_nomad_config = templatefile("${path.module}/templates/nomad-gpu.hcl.tftpl", {
    main_private_ip = aws_instance.this.private_ip
  })
}

data "cloudinit_config" "gpu" {
  count = var.enable_gpu_node ? 1 : 0

  gzip          = true
  base64_encode = true

  part {
    filename     = "bootstrap-gpu.sh"
    content_type = "text/x-shellscript"
    content = templatefile("${path.module}/templates/bootstrap-gpu.sh.tftpl", {
      nomad_config  = local.gpu_nomad_config
      nomad_service = file("${path.root}/config/nomad.service")
      nomad_crt     = tls_self_signed_cert.nomad.cert_pem
      nomad_key     = tls_private_key.nomad.private_key_pem
      nomad_license = var.nomad_license
    })
  }
}

resource "aws_instance" "gpu" {
  count = var.enable_gpu_node ? 1 : 0

  ami                         = data.aws_ami.gpu[0].id
  instance_type               = var.gpu_instance_type
  key_name                    = aws_key_pair.this.key_name
  subnet_id                   = aws_subnet.public[0].id
  vpc_security_group_ids      = [aws_security_group.instance.id]
  associate_public_ip_address = true
  user_data_base64            = data.cloudinit_config.gpu[0].rendered
  iam_instance_profile        = aws_iam_instance_profile.instance.name

  root_block_device {
    volume_size = var.gpu_root_volume_size
  }

  tags = {
    Name  = "${local.name}-gpu"
    owner = var.owner
  }

  # The server must be up (its private IP is baked into this node's user_data and
  # the client retry_joins it).
  depends_on = [
    aws_instance.this,
    aws_route_table_association.public,
  ]

  # Wait for the GPU client to register and the driver to be live. Direct SSH over
  # the public IP (the instance SG already allows SSH from the caller /32).
  provisioner "remote-exec" {
    inline = [
      "echo 'Waiting for GPU node bootstrap to finish...'",
      "timeout 900 bash -c 'until [ -f /home/ubuntu/.gpu-ready ]; do sleep 10; done' || { echo 'gpu bootstrap did not complete'; sudo tail -n 100 /var/log/gpu-bootstrap.log; exit 1; }",
      "nvidia-smi --query-gpu=name,driver_version --format=csv,noheader",
    ]

    connection {
      host        = self.public_ip
      user        = "ubuntu"
      agent       = false
      private_key = tls_private_key.ssh.private_key_openssh
    }
  }
}
