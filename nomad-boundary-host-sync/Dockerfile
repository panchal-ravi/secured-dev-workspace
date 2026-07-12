# Multi-stage build for the Nomad→Boundary host-sync. Stage 1 compiles a static Go
# binary; stage 2 is a minimal non-root runtime. Build context is this directory.

# 1) Go binary — static (CGO off) so it runs on the slim runtime base.
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /host-sync ./cmd/sync

# 2) Runtime — non-root, tini as PID 1 (signal forwarding for graceful shutdown),
# CA certs for outbound TLS to the Nomad + Boundary APIs.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates tini \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --uid 10001 --shell /usr/sbin/nologin --no-create-home hostsync
COPY --from=build /host-sync /usr/local/bin/host-sync
USER 10001
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/host-sync"]
