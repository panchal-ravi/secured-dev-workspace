# spike/header-echo/spike-header-echo.nomad.hcl — THROWAWAY (Phase 0, spike 1).
# A header-echo MCP server in the infra namespace, static port 4555 (free;
# 4444/4000/15432/15433 taken). Reachable on the node-private IP, inside the MCP
# gateway's SSRF VPC-CIDR allowlist so it can federate as a peer. server.py is
# embedded via a template stanza so the job is self-contained. No Vault, no service
# registration needed. Purge with: nomad job stop -namespace infra -purge spike-header-echo
job "spike-header-echo" {
  namespace   = "infra"
  datacenters = ["dc1"]
  type        = "service"

  group "echo" {
    count = 1

    network {
      port "mcp" {
        static = 4555
        to     = 4555
      }
    }

    task "server" {
      driver = "docker"

      config {
        image   = "python:3.12-slim"
        ports   = ["mcp"]
        command = "sh"
        args    = ["-c", "pip install --quiet 'mcp[cli]' uvicorn && python /local/server.py"]
      }

      template {
        destination = "local/server.py"
        change_mode = "restart"
        data        = <<EOH
from contextvars import ContextVar
from mcp.server.fastmcp import FastMCP
import uvicorn

_headers: ContextVar[dict] = ContextVar("headers", default={})
mcp = FastMCP("header-echo")

@mcp.tool()
def echo_headers() -> dict:
    """Return the HTTP headers this server received for the current request."""
    return _headers.get()

class Capture:
    def __init__(self, app): self.app = app
    async def __call__(self, scope, receive, send):
        if scope["type"] == "http":
            _headers.set({k.decode(): v.decode() for k, v in scope["headers"]})
        await self.app(scope, receive, send)

app = Capture(mcp.streamable_http_app())
uvicorn.run(app, host="0.0.0.0", port=4555)
EOH
      }

      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
