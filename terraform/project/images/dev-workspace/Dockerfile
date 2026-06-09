# dev-workspace:poc — the secured coding-workspace container image.
#
# glibc base (Debian) is required: the VSCode Remote-SSH server is glibc-only.
# Connectivity is SSH (CA-cert-only) brokered by Boundary; no code-server (Remote-SSH
# pushes its own server). Built on the node with `docker buildx build` — see
# terraform/project/README.md. The non-root `dev` user is uid/gid 1000 to match the
# dynamic host volume's owner so the persistent /home/dev is writable.
FROM debian:bookworm-slim

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
# the job entrypoint — see terraform/project/templates/dev-workspace.nomad.hcl. Here
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

# Non-root developer (uid 1000 == dynamic host volume owner) with PoC sudo.
RUN useradd --create-home --uid 1000 --shell /bin/bash dev \
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
