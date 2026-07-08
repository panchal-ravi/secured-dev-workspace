# Agent runtime image: one container = one deep-agent chat server. Built and
# pushed by build-image.sh. The agents node pool is amd64, so build linux/amd64.
FROM python:3.12-slim

# tini as PID 1 (clean signal handling for Nomad stop/restart), curl for the
# container HEALTHCHECK, a non-root user for the runtime.
RUN apt-get update && apt-get install -y --no-install-recommends \
      curl tini \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --uid 10001 --shell /usr/sbin/nologin --no-create-home agent

WORKDIR /app
RUN pip install --no-cache-dir --upgrade pip
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY main.py .

EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 \
  CMD curl -fs http://localhost:8000/healthz || exit 1
USER 10001
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
