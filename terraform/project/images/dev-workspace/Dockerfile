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

# Point Claude Code at DeepSeek instead of Anthropic. The non-secret config (the
# Anthropic-compatible base URL + DeepSeek model names, per DeepSeek's Claude Code
# integration docs) is baked here as enterprise-managed settings — highest
# precedence, project-enforced, and OUTSIDE /home/dev (which the persistent volume
# shadows). The API key is NOT baked: apiKeyHelper reads it at call time from the
# Vault-minted /secrets/deepseek-key (rendered per session by the job template), so
# the secret never lands on disk. Confirm the model ids against your DeepSeek
# dashboard — they are easy to change here.
RUN mkdir -p /etc/claude-code \
  && printf '%s\n' \
    '{' \
    '  "apiKeyHelper": "/usr/local/bin/deepseek-key",' \
    '  "env": {' \
    '    "ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",' \
    '    "ANTHROPIC_MODEL": "deepseek-v4-pro[1m]",' \
    '    "ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek-v4-pro[1m]",' \
    '    "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-pro[1m]",' \
    '    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek-v4-flash",' \
    '    "CLAUDE_CODE_SUBAGENT_MODEL": "deepseek-v4-flash",' \
    '    "CLAUDE_CODE_EFFORT_LEVEL": "max"' \
    '  }' \
    '}' \
    > /etc/claude-code/managed-settings.json \
  && printf '%s\n' '#!/bin/sh' 'cat /secrets/deepseek-key' > /usr/local/bin/deepseek-key \
  && chmod 0755 /usr/local/bin/deepseek-key

# Postgres MCP server (Vault use case B1), installed at BUILD time via uv into
# system paths so it runs OFFLINE at runtime (the workspace has restricted egress)
# and OUTSIDE /home/dev (which the persistent volume shadows). uv fetches its own
# Python 3.12 because postgres-mcp needs >=3.12 (bookworm ships 3.11). Pinned.
ENV UV_PYTHON_INSTALL_DIR=/opt/uv/python \
    UV_TOOL_DIR=/opt/uv/tools \
    UV_TOOL_BIN_DIR=/usr/local/bin
RUN python3 -m pip install --no-cache-dir --break-system-packages uv \
  && uv python install 3.12 \
  && uv tool install --python 3.12 postgres-mcp==0.3.0 \
  && /usr/local/bin/postgres-mcp --help >/dev/null \
  && chmod -R a+rX /opt/uv

# Wrapper Claude's MCP config invokes (registered per-workspace by the job
# entrypoint as `claude mcp add ... -- /usr/local/bin/pg-mcp`): it feeds
# DATABASE_URI from the Vault-minted, per-session credential rendered to the
# /secrets tmpfs, then execs the server read-only. Read at spawn, so each session
# uses the current credential.
RUN printf '%s\n' '#!/bin/sh' \
  'export DATABASE_URI="$(cat /secrets/db-uri)"' \
  'exec /usr/local/bin/postgres-mcp --access-mode=restricted' \
  > /usr/local/bin/pg-mcp \
  && chmod 0755 /usr/local/bin/pg-mcp

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
