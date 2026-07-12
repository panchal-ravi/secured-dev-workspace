# Agent worker AMI (Packer)

Builds the AMI for the **agent worker** nodes (standard CPU) that join the
all-in-one node in node pool `agents`. Like the GPU image, this node is a Nomad
**client only** — no Consul / Boundary / Vault binaries.

This is the **gpu_image with the entire NVIDIA stack removed**: no driver, no
`nvidia-container-toolkit`, no `nomad-device-nvidia` plugin, and no
reboot/`nvidia-smi` gate. The agent-platform tier schedules agent instances and
wrapped MCP servers here (`node_pool = "agents"`), and those are plain
containers — they need none of the GPU layers.

Built on the **same org base image as the base/gpu AMI**
(`hc-base-ubuntu-2404-amd64-*`, owner `888995627335`) so all nodes share one
Ubuntu 24.04 lineage. On top of it, it bakes in:

- Docker (engine, CLI, buildx) + CNI plugins + `br_netfilter`
- **Nomad** `1.11.6+ent` (matches the server) + `/opt/nomad/data`, `/var/log/nomad`

The resulting AMI is named `<owner>-agent-node-<timestamp>`. The agent instances
in `modules/secured-codespace` (`agent-nodes.tf`) look it up by
`<owner>-agent-node-*` (`owners = ["self"]`).

## Build

```bash
cd terraform/infra/ami/agent_image
packer init .
packer validate -var-file=variables.pkrvars.hcl .
packer build    -var-file=variables.pkrvars.hcl .
```

> Builds on any **standard CPU instance** (`t3.large`, the default) — no GPU or
> bare-metal host needed. Pass the directory (`.`), not just the `.pkr.hcl` file,
> so `variables.pkr.hcl` is loaded. Requires AWS credentials that can run an EC2
> instance and register an AMI.

Edit `variables.pkrvars.hcl` to change the owner, region, build instance type,
Nomad version, or the CNI release. Keep `nomad_version` in lockstep with the
server node so the client can join.
