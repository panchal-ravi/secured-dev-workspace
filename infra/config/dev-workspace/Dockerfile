# dev-workspace:poc — the secured coding-workspace container image.
#
# glibc base (Debian) is required: the VSCode Remote-SSH server is glibc-only.
# Connectivity is SSH (CA-cert-only) brokered by Boundary; no code-server (Remote-SSH
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
