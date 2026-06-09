# gpu-workspace:poc — the secured coding-workspace container image, GPU variant.
#
# Full dev-workspace parity (cert-only sshd, Claude Code, dynamic Git PAT, read-only
# DB MCP) on an NVIDIA CUDA devel base so the workspace has the CUDA toolchain (nvcc)
# for the GPU node (NVIDIA T4). The base is Ubuntu 24.04 (glibc — the VSCode Remote-SSH
# server is glibc-only) with CUDA preinstalled; the GPU itself is exposed at runtime by
# the nomad-device-nvidia plugin + the docker "nvidia" runtime (see the job template).
# Built on the node with `docker buildx build` — see terraform/project/README.md. The
# non-root `dev` user is uid/gid 1000 to match the dynamic host volume's owner.
FROM nvidia/cuda:12.6.3-devel-ubuntu24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server \
      git \
      curl \
      ca-certificates \
      sudo \
      build-essential \
      python3 \
      python3-pip \
  && rm -rf /var/lib/apt/lists/*

# Claude Code CLI (AI coding assistant), installed globally so `claude` is on PATH at
# /usr/bin — outside /home/dev, which the persistent dynamic host volume shadows at
# runtime. Node 20 LTS via NodeSource (Debian bookworm ships EOL Node 18). Version pinned.
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
  && apt-get install -y --no-install-recommends nodejs \
  && npm install -g @anthropic-ai/claude-code@2.1.161 \
  && claude --version \
  && rm -rf /var/lib/apt/lists/*

# Route Claude Code through the shared LiteLLM AI gateway (not the model provider
# directly). The gateway URL + model mapping live in Claude's enterprise-managed
# settings, but those are rendered PER WORKSPACE at job launch (the gateway address
# is deployment-specific) and installed to /etc/claude-code/managed-settings.json by
# the job entrypoint — see terraform/project/templates/gpu-workspace.nomad.hcl. Here
# we only create the dir and the apiKeyHelper: it serves the per-project LiteLLM
# virtual key from the Vault-minted /secrets/llm-key (rendered per session), so the
# key is read at call time and never lands on disk. The real provider key never
# reaches the workspace — it lives only on the gateway.
RUN mkdir -p /etc/claude-code \
  && printf '%s\n' '#!/bin/sh' 'cat /secrets/llm-key' > /usr/local/bin/llm-key \
  && chmod 0755 /usr/local/bin/llm-key

# The demo-db MCP server is no longer embedded in the image. Claude connects to
# the project's REMOTE virtual MCP server on the central ContextForge gateway —
# the job entrypoint registers it with `claude mcp add --transport sse` using the
# URL + bearer token rendered to /secrets from Vault. No local Python MCP
# toolchain (postgres-mcp / pg-mcp / uv) is baked in.

# Put the CUDA toolchain on the PATH for SSH sessions. The base image sets PATH/
# LD_LIBRARY_PATH via Docker ENV, but an sshd login shell does NOT inherit Docker ENV,
# so `nvcc` (at /usr/local/cuda/bin) and libcudart (at /usr/local/cuda/lib64) are
# invisible to the developer over Boundary SSH. A profile.d drop-in is sourced by the
# login shell, restoring both for interactive sessions.
RUN printf '%s\n' \
    'export PATH=/usr/local/cuda/bin${PATH:+:${PATH}}' \
    'export LD_LIBRARY_PATH=/usr/local/cuda/lib64${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}' \
    > /etc/profile.d/cuda.sh \
  && chmod 0644 /etc/profile.d/cuda.sh

# Non-root developer (uid 1000 == dynamic host volume owner) with PoC sudo.
# Ubuntu 24.04 (the CUDA base) ships a default `ubuntu` user/group at 1000 — remove it
# first so `dev` can claim uid/gid 1000 (debian-based dev-workspace has no such user).
RUN userdel --remove ubuntu 2>/dev/null || true; groupdel ubuntu 2>/dev/null || true; \
  useradd --create-home --uid 1000 --shell /bin/bash dev \
  && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
  && chmod 0440 /etc/sudoers.d/dev \
  && install -d -o dev -g dev -m 0700 /home/dev/.ssh \
  && install -d -m 0755 /run/sshd

# Cert-only sshd. sshd trusts the Vault SSH CA via TrustedUserCAKeys (the CA
# public key is installed to /etc/ssh/trusted_ca.pub at job launch); Boundary
# injects a short-lived CA-signed user cert per session. AuthorizedKeysFile is
# "none" so static authorized_keys are ignored entirely — even any left on a
# persistent home volume from the pre-JIT era; passwords are disabled.
RUN sed -ri 's/^#?PasswordAuthentication.*/PasswordAuthentication no/'  /etc/ssh/sshd_config \
  && sed -ri 's/^#?PubkeyAuthentication.*/PubkeyAuthentication yes/'    /etc/ssh/sshd_config \
  && sed -ri 's/^#?PermitRootLogin.*/PermitRootLogin no/'              /etc/ssh/sshd_config \
  && echo 'TrustedUserCAKeys /etc/ssh/trusted_ca.pub' >> /etc/ssh/sshd_config \
  && echo 'AuthorizedKeysFile none' >> /etc/ssh/sshd_config

EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
