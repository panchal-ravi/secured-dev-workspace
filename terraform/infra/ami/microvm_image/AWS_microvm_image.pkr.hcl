// microVM worker AMI for the secured-codespace.
//
// Builds an Ubuntu 24.04 image that joins the all-in-one node as a Nomad CLIENT
// in node_pool "microvm" (no server/Consul/Boundary/Vault — those stay on the
// main node). Bakes in: Docker + CNI, Kata Containers (a hardware-virtualized
// container runtime — each container runs in its own microVM with a separate
// guest kernel), and Nomad. Kata is registered as a NON-default docker runtime
// named "kata"; the microvm-workspace job opts in with runtime = "kata".
//
// The build runs two gates so a broken image never ships:
//   1. /dev/kvm present + `kata-runtime check` — the whole reason this builds on
//      a *.metal instance (a Nitro guest has no nested KVM, exactly as the GPU
//      image must build on a real T4).
//   2. `docker run --runtime=kata hello-world` — proves the dockerd + Kata-OCI
//      path (the highest-risk integration point) actually launches a microVM.
//
// The resulting AMI is named "<owner>-microvm-workspace-<timestamp>"; the
// modules/secured-codespace microVM node looks it up by
// "<owner>-microvm-workspace-*" (owners=["self"]). Build on a bare-metal
// instance (c5.metal) — the KVM gate needs real hardware virtualization.

packer {
  required_plugins {
    amazon = {
      version = ">= 1.2.8"
      source  = "github.com/hashicorp/amazon"
    }
  }
}

# Org base image — the SAME source the base AMI (ami/base_image/AWS_linux_image.pkr.hcl)
# builds from, so the microVM node shares the org-blessed Ubuntu 24.04 lineage (org
# hardening/tooling) rather than a raw Canonical image. The microVM node is still a Nomad
# CLIENT only; the provisioners below layer on Docker/CNI + Kata + Nomad (hc-base does not
# ship Docker or Kata, so those steps are still required).
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
  ami_name      = "${var.owner}-microvm-workspace-{{timestamp}}"
  region        = "${var.aws_region}"
  instance_type = var.aws_instance_type

  # Kata bundles a guest kernel + rootfs image; give the build (and resulting AMI) room.
  launch_block_device_mappings {
    device_name           = "/dev/sda1"
    volume_size           = 60
    volume_type           = "gp3"
    delete_on_termination = true
  }

  # Bare-metal instances stop slowly (often >10 min) when Packer stops them to snapshot
  # the AMI. Packer's default stop waiter (~10 min) times out on c5.metal, so extend it
  # to ~40 min (80 * 30s). Without this, provisioning succeeds but AMI creation fails.
  aws_polling {
    delay_seconds = 30
    max_attempts  = 80
  }

  tags = {
    Name          = "${var.owner}-microvm-workspace"
    nomad_version = "${var.nomad_version}"
    kata_version  = "${var.kata_version}"
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
  #    bridge networking work identically on the microVM node.
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
      // Pin docker-ce to ${var.docker_version}: Docker 28/29 (containerd 2.x) breaks the
      // Kata shim ("invalid namespace type"); 27.5.1 launches Kata microVMs correctly.
      "sudo apt-get install -y docker-ce=${var.docker_version} docker-ce-cli=${var.docker_version} containerd.io docker-buildx-plugin docker-compose-plugin",
      "sudo apt-mark hold docker-ce docker-ce-cli",

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

  # 2. Kata Containers via the official kata-static tarball (self-contained: it ships
  #    its own QEMU, guest kernel, and rootfs image under /opt/kata). Pinned by
  #    var.kata_version. Two binaries are put on PATH:
  #      - containerd-shim-kata-v2 — the actual runtime entry point (a containerd v2
  #        shim). dockerd's embedded containerd execs it by name (derived from the
  #        runtime type io.containerd.kata.v2 registered in daemon.json, step 3).
  #      - kata-runtime — the management CLI (used by `kata-runtime check` gates).
  #    Kata 3.x's kata-runtime is NOT an OCI runc-style binary, so it must NOT be
  #    registered as a docker `path` runtime (dockerd would call `kata-runtime create`
  #    and fail) — the shim + runtimeType is the correct wiring.
  #    Pin the qemu config (configuration.toml -> configuration-qemu.toml) so the shim
  #    defaults to QEMU — the hypervisor that supports virtio-fs, which the Nomad
  #    host-volume bind mount of /home/dev (and the tmpfs secrets/) need.
  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline = [
      "curl -fsSL -o kata-static.tar.xz \"https://github.com/kata-containers/kata-containers/releases/download/${var.kata_version}/kata-static-${var.kata_version}-amd64.tar.xz\"",
      "sudo tar -xJf kata-static.tar.xz -C /", // extracts to /opt/kata
      "sudo ln -sf /opt/kata/bin/containerd-shim-kata-v2 /usr/local/bin/containerd-shim-kata-v2",
      "sudo ln -sf /opt/kata/bin/kata-runtime /usr/local/bin/kata-runtime",
      "sudo ln -sf /opt/kata/share/defaults/kata-containers/configuration-qemu.toml /opt/kata/share/defaults/kata-containers/configuration.toml",
      "sudo sed -i 's/^shared_fs = .*/shared_fs = \"virtio-fs\"/' /opt/kata/share/defaults/kata-containers/configuration-qemu.toml",
    ]
  }

  # 3. Register "kata" as a NON-default docker runtime (the dockerd analogue of
  #    `nvidia-ctk runtime configure`). Kata 3.x is a containerd v2 shim, so this is a
  #    runtimeType runtime (dockerd's embedded containerd execs containerd-shim-kata-v2
  #    from PATH), NOT a runc-style OCI `path` binary. The microvm-workspace job sets
  #    runtime = "kata"; runc stays the default so the node's other tasks are unaffected.
  #
  #    default-cgroupns-mode = "host": on cgroup-v2 hosts Docker defaults to a PRIVATE
  #    cgroup namespace, which it adds to the OCI spec. Kata's runtime rejects that
  #    (`failed to create shim task: invalid namespace type`), so force host cgroupns
  #    daemon-wide. The workload runs inside a VM, so cgroup-ns isolation on the host
  #    side is moot here — and this is the only knob Nomad's docker driver can't set
  #    per-task, so it must live in the daemon config.
  provisioner "shell" {
    inline = [
      "echo '{ \"runtimes\": { \"kata\": { \"runtimeType\": \"io.containerd.kata.v2\" } }, \"default-cgroupns-mode\": \"host\" }' | sudo tee /etc/docker/daemon.json",
      "sudo systemctl restart docker",
    ]
  }

  # 4a. KVM gate: fail the build unless this host exposes hardware virtualization.
  #     This is the whole point of building on a *.metal instance — a Nitro guest
  #     (t3/g4dn-non-metal) has no /dev/kvm, so a microVM image built there would be
  #     dead on arrival.
  provisioner "shell" {
    inline = [
      "echo 'Verifying hardware virtualization is available...'",
      "test -e /dev/kvm || { echo 'NO /dev/kvm — build host is not bare metal'; exit 1; }",
      "sudo /usr/local/bin/kata-runtime check",
    ]
  }

  # 4b. Smoke gate: launch a real container under the kata runtime. This proves the
  #     dockerd -> containerd-shim-kata-v2 path end to end (image rootfs shared into
  #     the guest, guest kernel boots, process runs) — the highest-risk integration
  #     point, caught here before the AMI is ever used.
  provisioner "shell" {
    inline = [
      // No --cgroupns flag: rely on the daemon's default-cgroupns-mode=host (step 3),
      // because that is exactly what Nomad's docker driver will hit (it can't set cgroupns
      // per-task). This gates the real workspace path, not just an explicit-flag path.
      "echo 'Smoke-testing the kata runtime under docker...'",
      "sudo docker run --rm --runtime=kata hello-world",
      // Prove the microVM boundary: the guest runs a DIFFERENT kernel than the host.
      // If these match, the container ran under runc (shared kernel), not Kata — fail.
      "HOST_KERNEL=$(uname -r); echo \"host kernel: $HOST_KERNEL\"",
      "GUEST_KERNEL=$(sudo docker run --rm --runtime=kata alpine uname -r); echo \"guest kernel: $GUEST_KERNEL\"",
      "[ \"$HOST_KERNEL\" != \"$GUEST_KERNEL\" ] || { echo 'guest kernel == host kernel — NOT isolated in a microVM'; exit 1; }",
      "echo 'OK: kata microVM runs a separate guest kernel'",
    ]
  }

  # 5. Nomad binary + user/dirs (mirrors the base/GPU image; the bootstrap runs the
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
}
