# ---------------------------------------------------------------------------
# microVM worker node — a bare-metal EC2 (c5.metal) that joins the all-in-one
# node as a Nomad CLIENT in node_pool "microvm". The portal schedules the
# microvm-workspace template here; the co-located Boundary worker on the main
# node dials this node's published SSH host ports over the VPC (the 2222-2399 SG
# rule in network.tf). No new Vault/Boundary/Postgres — those stay on the main
# node. Workspaces run inside a Kata Containers microVM (separate guest kernel,
# KVM boundary) — the isolation upgrade over shared-kernel container namespaces,
# which is why the node must be bare metal (a Nitro guest has no nested KVM).
# ---------------------------------------------------------------------------

# AMI built by ami/microvm_image (Ubuntu 24.04 + Docker/CNI + Kata Containers +
# the "kata" docker runtime + Nomad).
data "aws_ami" "microvm" {
  count = var.enable_microvm_node ? 1 : 0

  most_recent = true
  owners      = ["self"]

  filter {
    name   = "name"
    values = ["${var.owner}-microvm-workspace-*"]
  }
}

locals {
  # Rendered with the all-in-one node's private IP so the client knows where to
  # retry_join the server and reach Vault.
  microvm_nomad_config = templatefile("${path.module}/templates/nomad-microvm.hcl.tftpl", {
    main_private_ip = aws_instance.this.private_ip
  })
}

data "cloudinit_config" "microvm" {
  count = var.enable_microvm_node ? 1 : 0

  gzip          = true
  base64_encode = true

  part {
    filename     = "bootstrap-microvm.sh"
    content_type = "text/x-shellscript"
    content = templatefile("${path.module}/templates/bootstrap-microvm.sh.tftpl", {
      nomad_config  = local.microvm_nomad_config
      nomad_service = file("${path.root}/config/nomad.service")
      nomad_crt     = tls_self_signed_cert.nomad.cert_pem
      nomad_key     = tls_private_key.nomad.private_key_pem
      nomad_license = var.nomad_license
    })
  }
}

resource "aws_instance" "microvm" {
  count = var.enable_microvm_node ? 1 : 0

  ami                         = data.aws_ami.microvm[0].id
  instance_type               = var.microvm_instance_type
  key_name                    = aws_key_pair.this.key_name
  subnet_id                   = aws_subnet.public[0].id
  vpc_security_group_ids      = [aws_security_group.instance.id]
  associate_public_ip_address = true
  user_data_base64            = data.cloudinit_config.microvm[0].rendered
  iam_instance_profile        = aws_iam_instance_profile.instance.name

  root_block_device {
    volume_size = var.microvm_root_volume_size
  }

  tags = {
    Name  = "${local.name}-microvm"
    owner = var.owner
  }

  # The server must be up (its private IP is baked into this node's user_data and
  # the client retry_joins it).
  depends_on = [
    aws_instance.this,
    aws_route_table_association.public,
  ]

  # Wait for the microVM client to register and the kata runtime to be live. Direct
  # SSH over the public IP (the instance SG already allows SSH from the caller /32).
  provisioner "remote-exec" {
    inline = [
      "echo 'Waiting for microVM node bootstrap to finish...'",
      "timeout 900 bash -c 'until [ -f /home/ubuntu/.microvm-ready ]; do sleep 10; done' || { echo 'microvm bootstrap did not complete'; sudo tail -n 100 /var/log/microvm-bootstrap.log; exit 1; }",
      "sudo /usr/local/bin/kata-runtime check",
      "sudo docker run --rm --runtime=kata hello-world",
    ]

    connection {
      host        = self.public_ip
      user        = "ubuntu"
      agent       = false
      private_key = tls_private_key.ssh.private_key_openssh
    }
  }
}
