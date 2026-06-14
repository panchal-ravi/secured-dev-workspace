#!/usr/bin/env python3
"""Generate the agent-platform component status map (.excalidraw).

Regular persona-lane grid; status encoded by color. Computed layout guarantees
alignment (a generator is the right tool for a grid; the skill's no-generator
guidance targets organic 'arguing' diagrams, not status grids).
"""
import json

# palette (from skill color-palette.md)
GREEN = ("#a7f3d0", "#047857")   # built & verified
AMBER = ("#fef3c7", "#b45309")   # in progress (Phase 1)
GREY  = ("#dbeafe", "#1e40af")   # planned (Phase 2-6)
TITLE = "#1e40af"
BODY  = "#64748b"
INBOX = "#374151"

els = []
_seed = [1000]
def seed():
    _seed[0] += 7
    return _seed[0]

def text(x, y, w, h, s, size, color, align="left", valign="top"):
    els.append({
        "type": "text", "id": f"t{seed()}", "x": x, "y": y, "width": w, "height": h,
        "text": s, "originalText": s, "fontSize": size, "fontFamily": 3,
        "textAlign": align, "verticalAlign": valign, "strokeColor": color,
        "backgroundColor": "transparent", "fillStyle": "solid", "strokeWidth": 1,
        "strokeStyle": "solid", "roughness": 0, "opacity": 100, "angle": 0,
        "seed": seed(), "version": 1, "versionNonce": seed(), "isDeleted": False,
        "groupIds": [], "boundElements": None, "link": None, "locked": False,
        "containerId": None, "lineHeight": 1.25,
    })

def box(x, y, w, h, label, status):
    bg, st = status
    dashed = status is GREY
    els.append({
        "type": "rectangle", "id": f"r{seed()}", "x": x, "y": y, "width": w, "height": h,
        "strokeColor": st, "backgroundColor": bg, "fillStyle": "solid", "strokeWidth": 2,
        "strokeStyle": "dashed" if dashed else "solid", "roughness": 0, "opacity": 100,
        "angle": 0, "seed": seed(), "version": 1, "versionNonce": seed(),
        "isDeleted": False, "groupIds": [], "boundElements": None, "link": None,
        "locked": False, "roundness": {"type": 3},
    })
    # centered multi-line label (free-floating, manually centered)
    lines = label.count("\n") + 1
    th = lines * 19
    text(x, y + (h - th) / 2, w, th, label, 15, INBOX, align="center", valign="middle")

def divider(x, y, w):
    els.append({
        "type": "line", "id": f"l{seed()}", "x": x, "y": y, "width": w, "height": 0,
        "strokeColor": "#cbd5e1", "backgroundColor": "transparent", "fillStyle": "solid",
        "strokeWidth": 1, "strokeStyle": "dashed", "roughness": 0, "opacity": 100,
        "angle": 0, "seed": seed(), "version": 1, "versionNonce": seed(),
        "isDeleted": False, "groupIds": [], "boundElements": None, "link": None,
        "locked": False, "points": [[0, 0], [w, 0]],
    })

# ---- header ----
text(60, 36, 1500, 40, "Agent Platform — Components by Persona & Build Status", 30, TITLE)
text(60, 84, 1500, 24,
     "Green = built & verified on the live platform   ·   Amber = in progress (Phase 1)   ·   "
     "Grey (dashed) = planned (Phase 2–6)        as of 2026-06-14",
     15, BODY)

BW, BH = 215, 74
X0, STRIDE = 250, 240

lanes = [
    # (lane title lines, lane y, [(label, status), ...])
    ("Shared\nfoundation\n(Phase 0–1)", 150, [
        ("IBM Verify\nagent-token-exchange app", GREEN),
        ("Vault agent-identity\n+ jwt-obo mount", GREEN),
        ("token-exchange svc :4460\ncode + container ✓ → deploy", AMBER),
        ("Agent worker nodes\nnode pool: agents", GREEN),
        ("Portal rbac/ + store/\ncode ✓ (in-mem + postgres)", AMBER),
    ]),
    ("Developer\n(shipped\nbaseline)", 280, [
        ("Developer Portal\nGo + Carbon React", GREEN),
        ("Boundary · Nomad\nVault · Consul", GREEN),
        ("Workspaces\nCodespace / GPU / microVM", GREEN),
        ("LiteLLM gateway", GREEN),
        ("ContextForge\nMCP gateway", GREEN),
    ]),
    ("Platform admin\nonboarding\n(active — pulled fwd)", 410, [
        ("MCP server deploy\nform → test → publish", AMBER),
        ("LLM model onboard\nform → test → publish", AMBER),
        ("mcpgw/ + llmgw/\ngateway clients ✓", AMBER),
        ("admin/ service + API\n/api/admin/* ✓", AMBER),
        ("infra-mcp ns +\nLiteLLM DB flip", AMBER),
    ]),
    ("Business user\n/ Operator\n(deferred)", 540, [
        ("mcp-auth-wrapper (PEP)\nper-tool OBO→Vault JIT", GREY),
        ("Wrapped Postgres MCP\ncustomer-records", GREY),
        ("agent-runtime\ndeepagents + SSE chat", GREY),
        ("Portal: Agents +\nAgentChat UI", GREY),
    ]),
    ("Project admin +\ncopilot\n(deferred)", 670, [
        ("Virtual MCP servers\nContextForge compose", GREY),
        ("Agent templates\nADK-compatible YAML", GREY),
        ("Portal: ProjectAdmin UI\nmcpgw · mcpdiscovery", GREY),
        ("portal-admin-mcp +\nauthoring copilot", GREY),
    ]),
]

for title, y, comps in lanes:
    text(40, y + 6, 190, 80, title, 16, TITLE)
    for i, (label, status) in enumerate(comps):
        box(X0 + i * STRIDE, y, BW, BH, label, status)

# lane dividers (in the gaps between lanes)
for gy in (264, 394, 524, 654):
    divider(40, gy, 1880)

# footer
text(60, 768, 1820,
     22, "Build frontier: the Platform-Admin onboarding plane, pulled ahead of the wrapper/runtime/project-admin tracks. "
     "Amber = code built + unit-tested (not yet live); green = live on the platform today. Onboarding verifies each capability the "
     "way a Project Admin consumes it: MCP = virtual server + scoped token (200 + 403 isolation); LLM = scoped budgeted/rate-limited key.",
     14, BODY)

doc = {
    "type": "excalidraw", "version": 2, "source": "https://excalidraw.com",
    "elements": els,
    "appState": {"viewBackgroundColor": "#ffffff", "gridSize": 20},
    "files": {},
}
out = "/Users/ravipanchal/advanced-sa/secured-dev-workspace/diagrams/04-agent-platform-status.excalidraw"
with open(out, "w") as f:
    json.dump(doc, f, indent=2)
print("wrote", out, "elements:", len(els))
