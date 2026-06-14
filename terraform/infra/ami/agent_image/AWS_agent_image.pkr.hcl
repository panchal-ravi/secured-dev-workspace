// Agent worker AMI for the secured-codespace agent-platform tier.
//
// Builds an Ubuntu 24.04 image that joins the all-in-one node as a Nomad CLIENT
// in node_pool "agents" (no server/Consul/Boundary/Vault — those stay on the
// main node). Bakes in: Docker + CNI and Nomad. This is the gpu_image with the
// entire NVIDIA stack removed (no driver, no container-toolkit, no
// nomad-device-nvidia plugin, no reboot/nvidia-smi gate) — an agent instance or
// a wrapped MCP server container needs none of it.
//
// The resulting AMI is named "<owner>-agent-node-<timestamp>"; the
// modules/secured-codespace agent nodes look it up by "<owner>-agent-node-*"
// (owners=["self"]). Builds on any standard CPU instance (no GPU required).

packer {
  required_plugins {
    amazon = {
      version = ">= 1.2.8"
      source  = "github.com/hashicorp/amazon"
    }
  }
}

# Org base image — the SAME source the base AMI (ami/base_image/AWS_linux_image.pkr.hcl)
# and gpu_image build from, so the agent node shares the org-blessed Ubuntu 24.04
# lineage (org hardening/tooling) rather than a raw Canonical image. The agent node
# is a Nomad CLIENT only; the provisioners below layer on Docker/CNI + Nomad
# (hc-base does not ship Docker, so those steps are still required).
data "amazon-ami" "ubuntu_amd64" {
  filters = {
    name                = "hc-base-ubuntu-2404-amd64-*"
    state               = "available"
    root-device-type    = "ebs"
    virtualization-type = "hvm"
  }
  most_recent = true
  owners      = ["888995627335"]
  region      = "${var.aws_region}"
}

source "amazon-ebs" "ubuntu_amd64" {
  ami_name      = "${var.owner}-agent-node-{{timestamp}}"
  region        = "${var.aws_region}"
  instance_type = var.aws_instance_type

  launch_block_device_mappings {
    device_name           = "/dev/sda1"
    volume_size           = 40
    volume_type           = "gp3"
    delete_on_termination = true
  }

  tags = {
    Name          = "${var.owner}-agent-node"
    nomad_version = "${var.nomad_version}"
  }

  source_ami   = data.amazon-ami.ubuntu_amd64.id
  communicator = "ssh"
  ssh_username = "ubuntu"
}

build {
  sources = [
    "source.amazon-ebs.ubuntu_amd64"
  ]

  # 1. Base utilities + Docker CE (engine, CLI, buildx) + CNI plugins + br_netfilter.
  #    Mirrors the base/gpu image's Docker/CNI setup so the client's docker driver
  #    and bridge networking work identically on the agent node.
  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "echo 'debconf debconf/frontend select Noninteractive' | sudo debconf-set-selections",
      "sudo apt-get update",
      "sudo apt-get install -y unzip jq net-tools ca-certificates curl gnupg",

      // Docker
      "sudo install -m 0755 -d /etc/apt/keyrings",
      "curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg",
      "sudo chmod a+r /etc/apt/keyrings/docker.gpg",
      "echo \"deb [arch=\"$(dpkg --print-architecture)\" signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \"$(. /etc/os-release && echo \"$VERSION_CODENAME\")\" stable\" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null",
      "sudo apt-get update",
      "sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",

      // CNI plugins
      "curl -L -o cni-plugins.tgz \"https://github.com/containernetworking/plugins/releases/download/${var.cni_version}/cni-plugins-linux-amd64-${var.cni_version}.tgz\"",
      "sudo mkdir -p /opt/cni/bin",
      "sudo tar -C /opt/cni/bin -xzf cni-plugins.tgz",

      // br_netfilter so Nomad bridge networking sees iptables
      "sudo modprobe br_netfilter",
      "echo \"br_netfilter\" | sudo tee /etc/modules-load.d/br_netfilter.conf",
      "echo \"net.bridge.bridge-nf-call-arptables = 1\" | sudo tee -a /etc/sysctl.d/iptables.conf",
      "echo \"net.bridge.bridge-nf-call-ip6tables = 1\" | sudo tee -a /etc/sysctl.d/iptables.conf",
      "echo \"net.bridge.bridge-nf-call-iptables = 1\" | sudo tee -a /etc/sysctl.d/iptables.conf",
    ]
  }

  # 2. Nomad binary + user/dirs (mirrors the base/gpu image; the bootstrap runs the
  #    agent as root so docker/exec drivers work). No /opt/nomad/plugins — the agent
  #    node has no external device plugin.
  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "echo Downloading \"https://releases.hashicorp.com/nomad/${var.nomad_version}/nomad_${var.nomad_version}_linux_amd64.zip\"",
      "curl -k -O \"https://releases.hashicorp.com/nomad/${var.nomad_version}/nomad_${var.nomad_version}_linux_amd64.zip\"",
      "unzip -o nomad_${var.nomad_version}_linux_amd64.zip",
      "sudo mv nomad /usr/local/bin/nomad",
      "sudo adduser --system --group nomad || true",
      "sudo mkdir -p /etc/nomad.d/tls /opt/nomad/data /var/log/nomad",
      "sudo chown -R nomad:nomad /etc/nomad.d /var/log/nomad",
    ]
  }
}
