# token-exchange — RFC 8693 OBO exchange service. uv-based build, non-root,
# uvicorn on :4460. Build/push with scripts/build-image.sh.

# 1) Build stage — resolve deps into a venv with uv (deps layer cached separately
#    from the source so code changes don't re-resolve the dependency tree).
FROM python:3.12-slim-bookworm AS build
COPY --from=ghcr.io/astral-sh/uv:0.8.6 /uv /usr/local/bin/uv
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY . .
RUN uv sync --frozen --no-dev

# 2) Runtime — non-root, tini as PID 1 (signal forwarding for graceful shutdown),
#    curl for the container HEALTHCHECK. No build tools, no uv.
FROM python:3.12-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends curl tini \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --uid 10001 --shell /usr/sbin/nologin --no-create-home tokenx
WORKDIR /app
COPY --from=build /app /app
ENV PATH="/app/.venv/bin:$PATH"
EXPOSE 4460
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -fs http://localhost:4460/healthz || exit 1
USER 10001
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["uvicorn", "api.main:app", "--host", "0.0.0.0", "--port", "4460"]
