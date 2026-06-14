# ---------------------------------------------------------------------------
# Agent worker nodes — var.agent_node_count standard-CPU EC2s (default 1) that
# join the all-in-one node as Nomad CLIENTs in node_pool "agents". The agent-platform tier schedules agent
# instances + wrapped MCP servers here (the agent-runtime template and the
# mcp-auth-wrapper task groups opt into this pool) so they never load the
# all-in-one node. No new Vault/Boundary/Postgres — those stay on the main node;
# these are Nomad CLIENTs only, reaching the server + Vault over the VPC.
#
# Same optional-node pattern as gpu.tf / microvm.tf (tfvars-gated, default off),
# minus the NVIDIA stack and the bare-metal/Kata requirement: a plain Ubuntu
# 24.04 + Docker + CNI + Nomad client image is all an agent/MCP container needs.
# ---------------------------------------------------------------------------

# AMI built by ami/agent_image (Ubuntu 24.04 + Docker/CNI + Nomad client — the
# gpu_image minus the NVIDIA driver/toolkit/device-plugin layers). One lookup
# shared by both nodes.
data "aws_ami" "agent" {
  count = var.enable_agent_nodes ? 1 : 0

  most_recent = true
  owners      = ["self"]

  filter {
    name   = "name"
    values = ["${var.owner}-agent-node-*"]
  }
}

data "cloudinit_config" "agent" {
  count = var.enable_agent_nodes ? var.agent_node_count : 0

  gzip          = true
  base64_encode = true

  part {
    filename     = "bootstrap-agents.sh"
    content_type = "text/x-shellscript"
    content = templatefile("${path.module}/templates/bootstrap-agents.sh.tftpl", {
      # Rendered per node so each client gets a unique Nomad node name. The
      # all-in-one node's private IP is baked in so the client retry_joins the
      # server and reaches Vault over the VPC.
      nomad_config = templatefile("${path.module}/templates/nomad-agents.hcl.tftpl", {
        main_private_ip = aws_instance.this.private_ip
        count_index     = count.index
      })
      nomad_service = file("${path.root}/config/nomad.service")
      nomad_crt     = tls_self_signed_cert.nomad.cert_pem
      nomad_key     = tls_private_key.nomad.private_key_pem
      nomad_license = var.nomad_license
    })
  }
}

resource "aws_instance" "agent" {
  count = var.enable_agent_nodes ? var.agent_node_count : 0

  ami                         = data.aws_ami.agent[0].id
  instance_type               = var.agent_instance_type
  key_name                    = aws_key_pair.this.key_name
  subnet_id                   = aws_subnet.public[0].id
  vpc_security_group_ids      = [aws_security_group.instance.id]
  associate_public_ip_address = true
  user_data_base64            = data.cloudinit_config.agent[count.index].rendered

  root_block_device {
    volume_size = var.agent_root_volume_size
  }

  tags = {
    Name  = "${local.name}-agent-${count.index}"
    owner = var.owner
  }

  # The server must be up (its private IP is baked into this node's user_data and
  # the client retry_joins it).
  depends_on = [
    aws_instance.this,
    aws_route_table_association.public,
  ]

  # Wait for the agent client to register. Direct SSH over the public IP (the
  # instance SG already allows SSH from the caller /32). No nvidia-smi gate — the
  # agent node has no device plugin.
  provisioner "remote-exec" {
    inline = [
      "echo 'Waiting for agent node bootstrap to finish...'",
      "timeout 900 bash -c 'until [ -f /home/ubuntu/.agent-ready ]; do sleep 10; done' || { echo 'agent bootstrap did not complete'; sudo tail -n 100 /var/log/agent-bootstrap.log; exit 1; }",
      "echo 'Agent node ready.'",
    ]

    connection {
      host        = self.public_ip
      user        = "ubuntu"
      agent       = false
      private_key = tls_private_key.ssh.private_key_openssh
    }
  }
}
