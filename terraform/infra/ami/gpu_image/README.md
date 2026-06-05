# GPU worker AMI (Packer)

Builds the AMI for the **GPU Nomad client** node (NVIDIA T4 / g4dn.xlarge) that
joins the all-in-one node in node pool `gpu`. Unlike the base image, this node is
a Nomad **client only** — no Consul / Boundary / Vault binaries.

Built on the **same org base image as the base AMI** (`hc-base-ubuntu-2404-amd64-*`,
owner `888995627335`) so both nodes share one Ubuntu 24.04 lineage. On top of it,
it bakes in:

- Docker (engine, CLI, buildx) + CNI plugins + `br_netfilter`
- **NVIDIA datacenter driver** (`nvidia-driver-550-server`, DKMS-built against the
  `linux-aws` kernel). The build **reboots and runs `nvidia-smi`** as a gate — it
  fails if the kernel module didn't load, so a bad driver never ships in an AMI.
- **nvidia-container-toolkit** — `nvidia-ctk runtime configure --runtime=docker`
  registers the docker `nvidia` runtime (added to `/etc/docker/daemon.json`, **not**
  made the default). The `gpu-workspace` Nomad job opts in with `runtime = "nvidia"`.
- **Nomad** `1.11.6+ent` (matches the server) + `/opt/nomad/{data,plugins}`, `/var/log/nomad`
- **nomad-device-nvidia** plugin `1.1.0` in `/opt/nomad/plugins` — the GPU client
  config enables it so the node fingerprints its T4 and jobs can request
  `device "nvidia/gpu"`.

The resulting AMI is named `<owner>-gpu-workspace-<timestamp>`. The GPU instance in
`modules/secured-codespace` (`gpu.tf`) looks it up by `<owner>-gpu-workspace-*`
(`owners = ["self"]`). The CUDA toolchain itself (`nvcc`) is NOT on the host — it
ships inside the `gpu-workspace` container image.

## Build

```bash
cd terraform/infra/ami/gpu_image
packer init .
packer validate -var-file=variables.pkrvars.hcl .
packer build    -var-file=variables.pkrvars.hcl .
```

> Build on a **GPU instance** (`g4dn.xlarge`, the default) — the `nvidia-smi` gate
> needs a real T4 present. Pass the directory (`.`), not just the `.pkr.hcl` file,
> so `variables.pkr.hcl` is loaded. Requires AWS credentials that can run a g4dn
> instance and register an AMI.

Edit `variables.pkrvars.hcl` to change the owner, region, driver branch, Nomad
version, or the `nomad-device-nvidia` release. The `550-server` branch apt-resolves
to driver 580 (CUDA 13 capable), which forward-runs the `gpu-workspace` image's CUDA
12.6 base on the T4 (`sm_75`). Keep the driver new enough for the image's CUDA base.
