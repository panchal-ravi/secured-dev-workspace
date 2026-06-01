# ---------------------------------------------------------------------------
# Static AEAD KMS keys (root / worker-auth / recovery).
# Generated once in Terraform (held in state) so they are STABLE across reboots —
# regenerating them would orphan the DB-encrypted data. 32 raw bytes -> base64 is
# exactly what `kms "aead"` with aes-gcm expects.
# ---------------------------------------------------------------------------
resource "random_bytes" "root_key" {
  length = 32
}

resource "random_bytes" "worker_auth_key" {
  length = 32
}

resource "random_bytes" "recovery_key" {
  length = 32
}

# Password for the local Postgres `boundary` role. Alphanumeric (special = false)
# so it is safe to embed directly in the postgresql:// connection URL.
resource "random_password" "db" {
  length  = 24
  special = false
}

locals {
  db_url      = "postgresql://boundary:${random_password.db.result}@127.0.0.1:5432/boundary?sslmode=disable"
  public_addr = "${aws_lb.this.dns_name}:9202"

  boundary_config = templatefile("${path.root}/config/boundary-controller-worker.hcl.tpl", {
    db_url          = local.db_url
    public_addr     = local.public_addr
    root_key        = random_bytes.root_key.base64
    worker_auth_key = random_bytes.worker_auth_key.base64
    recovery_key    = random_bytes.recovery_key.base64
  })
}

data "cloudinit_config" "this" {
  gzip          = true
  base64_encode = true

  part {
    filename     = "bootstrap.sh"
    content_type = "text/x-shellscript"
    content = templatefile("${path.module}/templates/bootstrap.sh.tftpl", {
      db_password      = random_password.db.result
      boundary_config  = local.boundary_config
      boundary_crt     = tls_self_signed_cert.boundary.cert_pem
      boundary_key     = tls_private_key.boundary.private_key_pem
      boundary_service = file("${path.root}/config/boundary.service")
      boundary_license = var.boundary_license
      recovery_key     = random_bytes.recovery_key.base64
      admin_login_name = var.boundary_admin_login_name
      admin_password   = var.boundary_admin_password
      org_name         = var.boundary_org_name
      project_name     = var.boundary_project_name

      nomad_config  = file("${path.root}/config/nomad.hcl")
      nomad_service = file("${path.root}/config/nomad.service")
      nomad_crt     = tls_self_signed_cert.nomad.cert_pem
      nomad_key     = tls_private_key.nomad.private_key_pem
      nomad_license = var.nomad_license

      vault_config  = file("${path.root}/config/vault.hcl")
      vault_service = file("${path.root}/config/vault.service")
      vault_crt     = tls_self_signed_cert.vault.cert_pem
      vault_key     = tls_private_key.vault.private_key_pem
      vault_license = var.vault_license
    })
  }
}

resource "aws_instance" "this" {
  ami                         = data.aws_ami.this.id
  instance_type               = var.instance_type
  key_name                    = aws_key_pair.this.key_name
  subnet_id                   = aws_subnet.public[0].id
  vpc_security_group_ids      = [aws_security_group.instance.id]
  associate_public_ip_address = true
  user_data_base64            = data.cloudinit_config.this.rendered

  root_block_device {
    volume_size = var.root_volume_size
  }

  tags = {
    Name  = local.name
    owner = var.owner
  }

  lifecycle {
    ignore_changes = all
  }

  # Wait for cloud-init to finish bringing up Postgres + Boundary.
  provisioner "remote-exec" {
    inline = [
      "echo 'Waiting for Boundary bootstrap to finish...'",
      "timeout 1800 bash -c 'until [ -f /home/ubuntu/.boundary-ready ]; do sleep 10; done' || { echo 'bootstrap did not complete'; sudo tail -n 100 /var/log/boundary-bootstrap.log; exit 1; }",
    ]
  }

  # Pull the setup output (generated scope / auth-method / user / role IDs) back
  # to ./generated over the public IP (direct SSH, no bastion).
  provisioner "local-exec" {
    command = <<-EOT
      scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i ${path.root}/generated/${local.ssh_key_filename} \
        ubuntu@${self.public_ip}:/home/ubuntu/boundary-setup.json \
        ${path.root}/generated/boundary-setup.json
    EOT
  }

  # Pull the Nomad ACL bootstrap output (management token) back to ./generated.
  provisioner "local-exec" {
    command = <<-EOT
      scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i ${path.root}/generated/${local.ssh_key_filename} \
        ubuntu@${self.public_ip}:/home/ubuntu/nomad-setup.json \
        ${path.root}/generated/nomad-setup.json
    EOT
  }

  # Pull the Vault init output (root token + unseal keys) back to ./generated.
  provisioner "local-exec" {
    command = <<-EOT
      scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i ${path.root}/generated/${local.ssh_key_filename} \
        ubuntu@${self.public_ip}:/home/ubuntu/vault-setup.json \
        ${path.root}/generated/vault-setup.json
    EOT
  }

  connection {
    host        = self.public_ip
    user        = "ubuntu"
    agent       = false
    private_key = tls_private_key.ssh.private_key_openssh
  }

  depends_on = [
    local_file.private_key,
    aws_route_table_association.public,
  ]
}

# Read the scp'd setup output for the generated-ID outputs. depends_on defers the
# read to apply, after the instance's provisioners have written the file.
data "local_file" "boundary_setup" {
  filename   = "${path.root}/generated/boundary-setup.json"
  depends_on = [aws_instance.this]
}

# Read the scp'd Nomad ACL bootstrap output (management token) for the outputs.
data "local_file" "nomad_setup" {
  filename   = "${path.root}/generated/nomad-setup.json"
  depends_on = [aws_instance.this]
}

# Read the scp'd Vault init output (root token + unseal keys) for the outputs.
data "local_file" "vault_setup" {
  filename   = "${path.root}/generated/vault-setup.json"
  depends_on = [aws_instance.this]
}

# Remove the generated setup files on destroy.
resource "null_resource" "cleanup_setup" {
  triggers = {
    boundary_setup_path = "${path.root}/generated/boundary-setup.json"
    nomad_setup_path    = "${path.root}/generated/nomad-setup.json"
    vault_setup_path    = "${path.root}/generated/vault-setup.json"
  }

  provisioner "local-exec" {
    when    = destroy
    command = "rm -f ${self.triggers.boundary_setup_path} ${self.triggers.nomad_setup_path} ${self.triggers.vault_setup_path} || true"
  }
}
