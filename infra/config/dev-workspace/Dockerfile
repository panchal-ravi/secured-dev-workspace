# dev-workspace:poc — the secured coding-workspace container image.
#
# glibc base (Debian) is required: the VSCode Remote-SSH server is glibc-only.
# Connectivity is SSH (key-only) brokered by Boundary; no code-server (Remote-SSH
# pushes its own server). Built on the node with `docker buildx build` — see
# infra/workspace/README.md. The non-root `dev` user is uid/gid 1000 to match the
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

# Non-root developer (uid 1000 == dynamic host volume owner) with PoC sudo.
RUN useradd --create-home --uid 1000 --shell /bin/bash dev \
  && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
  && chmod 0440 /etc/sudoers.d/dev \
  && install -d -o dev -g dev -m 0700 /home/dev/.ssh \
  && install -d -m 0755 /run/sshd

# Key-only sshd. The developer's public key is injected at job launch by Nomad
# (template -> /home/dev/.ssh/authorized_keys); passwords are disabled.
RUN sed -ri 's/^#?PasswordAuthentication.*/PasswordAuthentication no/'  /etc/ssh/sshd_config \
  && sed -ri 's/^#?PubkeyAuthentication.*/PubkeyAuthentication yes/'    /etc/ssh/sshd_config \
  && sed -ri 's/^#?PermitRootLogin.*/PermitRootLogin no/'              /etc/ssh/sshd_config

EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
