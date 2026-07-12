# spike/header-echo/client.py — THROWAWAY (Phase 0, spike 1). pip install "mcp[cli]"
#
# Calls echo_headers through the ContextForge virtual server using a real MCP client
# (avoids the hand-rolled initialize/session handshake). This is also the exact shape
# scoped_tool will use later, so a PASS here de-risks Phase 2/3 too.
#
#   VS_URL=$GW_URL/servers/$VS_ID/mcp  CLIENT_TOKEN=...  python client.py
#   (use mcp.client.sse + the /sse path instead if only SSE is exposed on 1.0.2)
import asyncio
import os

from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client


async def main():
    url = os.environ["VS_URL"]
    headers = {
        "Authorization": f"Bearer {os.environ['CLIENT_TOKEN']}",
        "X-Upstream-Authorization": "Bearer spike-obo-test-123",
    }
    async with streamablehttp_client(url, headers=headers) as (r, w, _):
        async with ClientSession(r, w) as s:
            await s.initialize()
            print(await s.call_tool("echo_headers", {}))


if __name__ == "__main__":
    asyncio.run(main())
