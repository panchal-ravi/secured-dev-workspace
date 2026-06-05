packer {
  required_plugins {
    amazon = {
      version = ">= 1.2.8" // or latest
      source  = "github.com/hashicorp/amazon"
    }
  }
}

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
  ami_name      = "${var.owner}-boundary-enterprise-{{timestamp}}"
  region        = "${var.aws_region}"
  instance_type = var.aws_instance_type
  tags = {
    Name             = "${var.owner}-boundary-enterprise"
    boundary_version = "${var.boundary_version}"
    consul_version   = "${var.consul_version}"
    nomad_version    = "${var.nomad_version}"
    vault_version    = "${var.vault_version}"
  }
  # Reference the AMD64 data source
  source_ami   = data.amazon-ami.ubuntu_amd64.id
  communicator = "ssh"
  ssh_username = "ubuntu"
}

build {
  sources = [
    "source.amazon-ebs.ubuntu_amd64"
  ]

  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "echo 'debconf debconf/frontend select Noninteractive' | sudo debconf-set-selections",
      "curl -fsSL https://apt.releases.hashicorp.com/gpg | sudo gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg",
      "echo \"deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg arch=amd64] https://apt.releases.hashicorp.com $(lsb_release -cs) main\" | sudo tee /etc/apt/sources.list.d/hashicorp.list",
      "sudo apt-get update",
      "sudo apt-get install unzip -y",
      "sudo apt-get install default-jre -y",
      "sudo apt-get install net-tools -y",
      "sudo apt-get install jq -y",
      "sudo apt install zsh -y",
      "sh -c \"$(curl -fsSL https://raw.github.com/ohmyzsh/ohmyzsh/master/tools/install.sh)\"",
      "echo \"plugins=(git zsh-autosuggestions fast-syntax-highlighting)\" >> ~/.zshrc",
      "sudo usermod -s /usr/bin/zsh ubuntu",
    ]
  }

  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "sleep 10",
      "export ENVOY_VERSION_STRING=${var.envoy_version}",
      "curl -L https://func-e.io/install.sh | sudo bash -s -- -b /usr/local/bin",
      "export FUNC_E_PLATFORM=linux/amd64",
      "func-e use $ENVOY_VERSION_STRING",
      "sudo cp ~/.local/share/func-e/envoy-versions/${var.envoy_version}/bin/envoy /usr/local/bin/",
    ]
  }

  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      // Install docker
      "sudo apt-get install -y ca-certificates curl gnupg",
      "sudo install -m 0755 -d /etc/apt/keyrings",
      "curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg",
      "sudo chmod a+r /etc/apt/keyrings/docker.gpg",
      "echo \"deb [arch=\"$(dpkg --print-architecture)\" signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \"$(. /etc/os-release && echo \"$VERSION_CODENAME\")\" stable\" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null",
      "sudo apt-get update",
      "sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",

      // Install CNI Plugins
      "curl -L -o cni-plugins.tgz \"https://github.com/containernetworking/plugins/releases/download/${var.cni_version}/cni-plugins-linux-$( [ $(uname -m) = aarch64 ] && echo arm64 || echo amd64)\"-${var.cni_version}.tgz",
      "sudo mkdir -p /opt/cni/bin",
      "sudo tar -C /opt/cni/bin -xzf cni-plugins.tgz",

      // Install Consul-CNI plugin
      "curl -L -o consul-cni.zip \"https://releases.hashicorp.com/consul-cni/${var.consul_cni_version}/consul-cni_${var.consul_cni_version}_linux_$( [ $(uname -m) = aarch64 ] && echo arm64 || echo amd64)\".zip",
      "sudo unzip consul-cni.zip -d /opt/cni/bin -x LICENSE.txt",

      "sudo modprobe br_netfilter",
      "echo \"br_netfilter\" | sudo tee /etc/modules-load.d/br_netfilter.conf",
      "echo 1 | sudo tee /proc/sys/net/bridge/bridge-nf-call-arptables",
      "echo 1 | sudo tee /proc/sys/net/bridge/bridge-nf-call-ip6tables",
      "echo 1 | sudo tee /proc/sys/net/bridge/bridge-nf-call-iptables",
      "echo \"net.bridge.bridge-nf-call-arptables = 1\" | sudo tee -a /etc/sysctl.d/iptables.conf",
      "echo \"net.bridge.bridge-nf-call-ip6tables = 1\" | sudo tee -a /etc/sysctl.d/iptables.conf",
      "echo \"net.bridge.bridge-nf-call-iptables = 1\" | sudo tee -a /etc/sysctl.d/iptables.conf",

      // Download Nomad
      "echo Downloading \"https://releases.hashicorp.com/nomad/${var.nomad_version}/nomad_${var.nomad_version}_linux_amd64.zip\"",
      "curl -k -O \"https://releases.hashicorp.com/nomad/${var.nomad_version}/nomad_${var.nomad_version}_linux_amd64.zip\"",
      "unzip -o nomad_${var.nomad_version}_linux_amd64.zip",
      "sudo mv nomad /usr/local/bin/nomad",
      "sudo adduser --system --group nomad || true",
      "sudo mkdir -p /etc/nomad.d/data",
      "sudo chown -R nomad:nomad /etc/nomad.d",
      "sudo chown nomad:nomad /usr/local/bin/nomad",
      "sudo mkdir -p /var/log/nomad",
      "sudo chown -R nomad:nomad /var/log/nomad",
    ]
  }


  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "sleep 5",
      // Download Consul
      "echo Downloading \"https://releases.hashicorp.com/consul/${var.consul_version}/consul_${var.consul_version}_linux_amd64.zip\"",
      "curl -k -O \"https://releases.hashicorp.com/consul/${var.consul_version}/consul_${var.consul_version}_linux_amd64.zip\"",
      "unzip -o consul_${var.consul_version}_linux_amd64.zip",
      "sudo mv consul /usr/local/bin/consul",
      "sudo adduser --system --group consul || true",
      "sudo mkdir -p /etc/consul.d/data",
      "sudo chown -R consul:consul /etc/consul.d",
      "sudo chown consul:consul /usr/local/bin/consul",
      "sudo mkdir -p /var/log/consul",
      "sudo chown -R consul:consul /var/log/consul",
    ]
  }

  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "sleep 5",
      // Download Vault
      "echo Downloading \"https://releases.hashicorp.com/vault/${var.vault_version}/vault_${var.vault_version}_linux_amd64.zip\"",
      "curl -k -O \"https://releases.hashicorp.com/vault/${var.vault_version}/vault_${var.vault_version}_linux_amd64.zip\"",
      "unzip -o vault_${var.vault_version}_linux_amd64.zip",
      "sudo mv vault /usr/local/bin/vault",
      "sudo adduser --system --group vault || true",
      "sudo mkdir -p /etc/vault.d/data",
      "sudo mkdir -p /opt/vault/data",
      "sudo chown -R vault:vault /etc/vault.d /opt/vault",
      "sudo chown vault:vault /usr/local/bin/vault",
    ]
  }

  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "sleep 5",
      // Vault GitHub secrets plugin (external: martinbaillie/vault-plugin-secrets-github).
      // Mints short-lived GitHub App installation tokens for the dev-workspace git push
      // credential. Pinned + sha256-verified; registered into Vault's catalog by Terraform.
      "sudo apt-get install -y libcap2-bin",
      "sudo mkdir -p /opt/vault/plugins",
      "echo Downloading vault-plugin-secrets-github v${var.github_plugin_version}",
      "curl -fsSL -o vault-plugin-secrets-github \"https://github.com/martinbaillie/vault-plugin-secrets-github/releases/download/v${var.github_plugin_version}/vault-plugin-secrets-github-linux-amd64\"",
      "echo \"${var.github_plugin_sha256}  vault-plugin-secrets-github\" | sha256sum -c -",
      "sudo mv vault-plugin-secrets-github /opt/vault/plugins/vault-plugin-secrets-github",
      "sudo chmod 0755 /opt/vault/plugins/vault-plugin-secrets-github",
      "sudo chown -R vault:vault /opt/vault/plugins",
      // disable_mlock = false, so the plugin binary needs cap_ipc_lock to lock memory.
      "sudo setcap cap_ipc_lock=+ep /opt/vault/plugins/vault-plugin-secrets-github",
    ]
  }

  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "sleep 5",
      // Download Boundary
      "echo Downloading \"https://releases.hashicorp.com/boundary/${var.boundary_version}/boundary_${var.boundary_version}_linux_amd64.zip\"",
      "curl -k -O \"https://releases.hashicorp.com/boundary/${var.boundary_version}/boundary_${var.boundary_version}_linux_amd64.zip\"",
      "unzip -o boundary_${var.boundary_version}_linux_amd64.zip",
      "sudo mv boundary /usr/local/bin/boundary",
      "sudo adduser --system --group boundary || true",
      "sudo mkdir -p /etc/boundary.d/tls",
      "sudo chown -R boundary:boundary /etc/boundary.d",
      "sudo chown boundary:boundary /usr/local/bin/boundary",
      "sudo mkdir -p /var/log/boundary",
      "sudo chown -R boundary:boundary /var/log/boundary",
    ]
  }
}
