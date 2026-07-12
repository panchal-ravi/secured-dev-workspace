# spike/header-echo/server.py — THROWAWAY (Phase 0, spike 1). pip install "mcp[cli]" uvicorn
#
# A one-tool MCP server whose `echo_headers` returns the HTTP request headers it
# received. Used to prove whether ContextForge federates `X-Upstream-Authorization`
# through to a backend peer. Pure-ASGI middleware captures headers into a contextvar
# so we don't depend on a specific FastMCP request-context API. If the installed
# fastmcp/mcp SDK exposes get_http_headers()/request context, prefer that and record
# the API in findings R11.
from contextvars import ContextVar
from mcp.server.fastmcp import FastMCP
import uvicorn

_headers: ContextVar[dict] = ContextVar("headers", default={})
mcp = FastMCP("header-echo")


@mcp.tool()
def echo_headers() -> dict:
    """Return the HTTP headers this server received for the current request."""
    return _headers.get()


class Capture:  # ASGI middleware
    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] == "http":
            _headers.set({k.decode(): v.decode() for k, v in scope["headers"]})
        await self.app(scope, receive, send)


# Streamable-HTTP at /mcp. FastMCP also provides an SSE app factory (sse_app) if the
# spike needs to test what the gateway federates with — swap the wrapped app below.
app = Capture(mcp.streamable_http_app())

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=4555)
