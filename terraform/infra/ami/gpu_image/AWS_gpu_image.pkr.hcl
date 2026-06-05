// GPU worker AMI for the secured-codespace.
//
// Builds an Ubuntu 24.04 image that joins the all-in-one node as a Nomad CLIENT
// in node_pool "gpu" (no server/Consul/Boundary/Vault — those stay on the main
// node). Bakes in: Docker + CNI, the NVIDIA datacenter driver (validated with a
// reboot + nvidia-smi gate so the build fails if the kernel module didn't load),
// the nvidia-container-toolkit (registers the docker "nvidia" runtime, NOT the
// default), Nomad, and the nomad-device-nvidia plugin in /opt/nomad/plugins.
//
// The resulting AMI is named "<owner>-gpu-workspace-<timestamp>"; the
// modules/secured-codespace GPU node looks it up by "<owner>-gpu-workspace-*"
// (owners=["self"]). Build on a GPU instance (g4dn.xlarge) — the driver gate
// needs a real T4 present.

packer {
  required_plugins {
    amazon = {
      version = ">= 1.2.8"
      source  = "github.com/hashicorp/amazon"
    }
  }
}

# Org base image — the SAME source the base AMI (ami/base_image/AWS_linux_image.pkr.hcl)
# builds from, so the GPU node shares the org-blessed Ubuntu 24.04 lineage (org
# hardening/tooling) rather than a raw Canonical image. The GPU node is still a Nomad
# CLIENT only; the provisioners below layer on Docker/CNI + the NVIDIA stack + Nomad +
# the device plugin (hc-base does not ship Docker, so those steps are still required).
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
  ami_name      = "${var.owner}-gpu-workspace-{{timestamp}}"
  region        = "${var.aws_region}"
  instance_type = var.aws_instance_type

  # CUDA images are large; give the build (and resulting AMI) room.
  launch_block_device_mappings {
    device_name           = "/dev/sda1"
    volume_size           = 60
    volume_type           = "gp3"
    delete_on_termination = true
  }

  tags = {
    Name                = "${var.owner}-gpu-workspace"
    nomad_version       = "${var.nomad_version}"
    nvidia_driver       = "${var.nvidia_driver_package}"
    nomad_device_nvidia = "${var.nomad_device_nvidia_version}"
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
  #    Mirrors the base image's Docker/CNI setup so the client's docker driver and
  #    bridge networking work identically on the GPU node.
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

  # 2. NVIDIA datacenter driver via the CUDA apt keyring. DKMS builds the kernel
  #    module against the running linux-aws kernel, so install build-essential +
  #    matching headers first. Reboot afterwards so the module loads (step 3).
  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "sudo apt-get install -y build-essential dkms \"linux-headers-$(uname -r)\"",
      "curl -fsSL -O https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/cuda-keyring_1.1-1_all.deb",
      "sudo dpkg -i cuda-keyring_1.1-1_all.deb",
      "sudo apt-get update",
      "sudo apt-get install -y ${var.nvidia_driver_package}",
    ]
  }

  # 3a. Reboot so the freshly-built NVIDIA kernel module is loaded.
  provisioner "shell" {
    expect_disconnect = true
    inline            = ["echo 'Rebooting to load the NVIDIA kernel module...'; sudo reboot"]
  }

  # 3b. Gate: fail the build unless nvidia-smi reports the GPU after the reboot.
  #     This is the whole point of building on a g4dn (a real T4 must be present).
  provisioner "shell" {
    pause_before = "60s"
    inline = [
      "echo 'Verifying the NVIDIA driver loaded...'",
      "nvidia-smi",
      "nvidia-smi --query-gpu=name,driver_version --format=csv,noheader",
    ]
  }

  # 4. nvidia-container-toolkit — registers the docker "nvidia" runtime (added to
  #    /etc/docker/daemon.json but NOT made the default). The gpu-workspace job sets
  #    runtime = "nvidia" so the toolkit mounts the driver + devices into the container.
  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg",
      "curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list",
      "sudo apt-get update",
      "sudo apt-get install -y nvidia-container-toolkit",
      "sudo nvidia-ctk runtime configure --runtime=docker",
      "sudo systemctl restart docker",
    ]
  }

  # 5. Nomad binary + user/dirs (mirrors the base image; the bootstrap runs the
  #    agent as root so docker/exec drivers work).
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

  # 6. nomad-device-nvidia external device plugin -> /opt/nomad/plugins. The GPU
  #    client config sets plugin_dir + a plugin "nomad-device-nvidia" stanza so the
  #    node fingerprints its T4 and jobs can request device "nvidia/gpu".
  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "echo Downloading nomad-device-nvidia ${var.nomad_device_nvidia_version}",
      "curl -fsSL -o nomad-device-nvidia.zip \"https://releases.hashicorp.com/nomad-device-nvidia/${var.nomad_device_nvidia_version}/nomad-device-nvidia_${var.nomad_device_nvidia_version}_linux_amd64.zip\"",
      "sudo mkdir -p /opt/nomad/plugins",
      "sudo unzip -o nomad-device-nvidia.zip -d /opt/nomad/plugins",
      "sudo chmod 0755 /opt/nomad/plugins/nomad-device-nvidia",
      "sudo chown -R nomad:nomad /opt/nomad/plugins",
    ]
  }
}
