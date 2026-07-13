# Multi-stage build for the Developer Portal. The build context is portal/ (it
# needs both frontend/ and backend/). Stage 1 builds the Carbon SPA, stage 2
# compiles the Go backend, stage 3 assembles a minimal non-root runtime that
# serves the SPA + API. Build with portal/scripts/build-image.sh.

# 1) Frontend → the binary serves these static files at runtime.
FROM node:20-bookworm-slim AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci || npm install
COPY frontend/ ./
# vite.config builds to ../backend/web; copy that out to a clean /web for stage 3.
RUN npm run build && mkdir -p /web && cp -r ../backend/web/. /web/

# 2) Go binary — static (CGO off) so it runs on the slim runtime base.
FROM golang:1.25-bookworm AS backend
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /portal ./cmd/portal

# 3) Runtime — non-root, tini as PID 1 (signal forwarding for graceful shutdown),
# curl for the container HEALTHCHECK, CA certs for outbound TLS.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl tini \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --uid 10001 --shell /usr/sbin/nologin --no-create-home portal
WORKDIR /app
COPY --from=backend /portal /usr/local/bin/portal
COPY --from=frontend --chown=10001:10001 /web /app/web
# The secured-ws:// helper bundle, served at /helper/ from a sibling of the SPA dir
# (see internal/api/api.go). Built by portal/helper/macos/build.sh before the image.
COPY --chown=10001:10001 backend/helper-dist /app/helper-dist
ENV PORTAL_STATIC_DIR=/app/web \
    PORTAL_LISTEN_ADDR=:8443
EXPOSE 8443
# Works whether the portal serves HTTPS (production, self-signed → -k) or plain HTTP.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -fsk https://localhost:8443/health || curl -fs http://localhost:8443/health || exit 1
USER 10001
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/portal"]
