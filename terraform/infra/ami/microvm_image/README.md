# microVM worker AMI (Packer)

Builds the AMI for the **microVM Nomad client** node that joins the all-in-one
node in node pool `microvm`. Like the GPU node, this is a Nomad **client only** —
no Consul / Boundary / Vault binaries. Workspaces scheduled here run inside a
**Kata Containers microVM**: the same OCI image as the standard workspace, but
wrapped in its own lightweight VM with a **separate guest kernel** behind a
hardware-virtualization (KVM) boundary — the isolation upgrade over shared-kernel
container namespaces.

Built on the **same org base image as the base AMI** (`hc-base-ubuntu-2404-amd64-*`,
owner `888995627335`) so both nodes share one Ubuntu 24.04 lineage. On top of it,
it bakes in:

- Docker (engine, CLI, buildx) + CNI plugins + `br_netfilter`. **docker-ce is pinned to
  `27.5.1`** (and `apt-mark hold`): Docker 28/29 (which bundle containerd 2.x) break the
  Kata shim with `failed to create shim task: invalid namespace type`, while 27.5.1
  launches Kata microVMs correctly (verified on c5.metal). The standard/GPU nodes are
  unaffected and stay on current Docker — this pin is microVM-only.
- **Kata Containers** (`kata-static` tarball, pinned by `kata_version`, installed to
  `/opt/kata`). The tarball is self-contained — it ships its own QEMU, guest kernel,
  and rootfs image. The runtime is pinned to **QEMU + virtio-fs** so the Nomad
  host-volume bind mount of `/home/dev` (and the tmpfs `secrets/`) work inside the
  guest.
- A docker runtime named **`kata`** registered in `/etc/docker/daemon.json` (**not**
  the default; `runc` stays default). The `microvm-workspace` Nomad job opts in with
  `runtime = "kata"`.
- **Nomad** `1.11.6+ent` (matches the server) + `/opt/nomad/data`, `/var/log/nomad`

Two build gates fail the build if the image would be broken:

1. **KVM gate** — `/dev/kvm` present + `kata-runtime check`. This is why the build
   must run on a **bare-metal** instance: a Nitro guest has no nested virtualization,
   exactly as the GPU image must build on a real T4.
2. **Smoke gate** — `docker run --runtime=kata hello-world`. Proves the dockerd +
   Kata-OCI path actually launches a microVM (the highest-risk integration point).

The resulting AMI is named `<owner>-microvm-workspace-<timestamp>`. The microVM
instance in `modules/secured-codespace` (`microvm.tf`) looks it up by
`<owner>-microvm-workspace-*` (`owners = ["self"]`).

## Build

```bash
cd terraform/infra/ami/microvm_image
packer init .
packer validate -var-file=variables.pkrvars.hcl .
packer build    -var-file=variables.pkrvars.hcl .
```

> Build on a **bare-metal instance** (`c5.metal`, the default) — the KVM gate needs
> real hardware virtualization. Bare-metal instances are costly (~$4/hr), so build
> deliberately and terminate the builder promptly. Pass the directory (`.`), not just
> the `.pkr.hcl` file, so `variables.pkr.hcl` is loaded.

Edit `variables.pkrvars.hcl` to change the owner, region, Nomad version, or the Kata
release. Before building, check the [Kata Containers releases](https://github.com/kata-containers/kata-containers/releases)
for the latest stable `kata_version` and pin it.
