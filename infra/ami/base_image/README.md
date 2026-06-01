# Boundary Enterprise base image (Packer)

Builds an AWS AMI on top of the org base image `hc-base-ubuntu-2404-amd64-*`
(owner `888995627335`, region `ap-southeast-1`) and bakes in:

- Utilities: `unzip`, `default-jre`, `net-tools`, `jq`, `zsh` + Oh My Zsh
- Envoy (via `func-e`)
- Docker (engine, CLI, buildx, compose) + CNI plugins + Consul-CNI plugin
- **HashiCorp Enterprise binaries** installed to `/usr/local/bin`, each with a
  system user/group and config dir:
  - Boundary `0.21.3+ent` → `/etc/boundary.d`
  - Consul `1.22.8+ent` → `/etc/consul.d`
  - Nomad `1.11.6+ent` → `/etc/nomad.d`
  - Vault `1.20.4+ent` → `/etc/vault.d`, `/opt/vault`

The resulting AMI is named `<owner>-boundary-enterprise-<timestamp>` and tagged
with all four versions. The `modules/boundary-allinone` Terraform module looks it
up by the name filter `<owner>-boundary-enterprise-*` (`owners = ["self"]`).

## Build

```bash
cd infra/ami/base_image
packer init .
packer validate -var-file=variables.pkrvars.hcl .
packer build    -var-file=variables.pkrvars.hcl .
```

> Pass the directory (`.`), not just `AWS_linux_image.pkr.hcl` — the variable
> declarations live in `variables.pkr.hcl` and are only loaded with the directory.

Edit `variables.pkrvars.hcl` to change the owner, region, or any binary version.
Packer provisions over SSH as the `ubuntu` user. Requires AWS credentials in the
environment with permission to run an EC2 instance and register an AMI.
