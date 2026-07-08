"""Agent runtime: a single LangChain deep-agent served over HTTP.

One container runs one agent. Its behaviour is fully described by two files
mounted by the Nomad job (see internal/agentjob):

  * AGENT_CONFIG   (/local/agent.yaml)    — the power user's verbatim agent YAML.
  * AGENT_RUNTIME  (/secrets/runtime.yaml) — Vault-rendered secrets: the LiteLLM
    base URL + per-agent key, and each selected MCP server's URL + scoped token.

The agent is built once at startup and reused for every /chat request. Chat is
streamed as Server-Sent Events; the portal proxies the stream to the browser.
Conversation history lives in an in-memory checkpointer keyed by thread_id, so
it is lost on restart — an accepted v1 limitation.
"""

import json
import os

import yaml
from deepagents import HarnessProfile, create_deep_agent, register_harness_profile
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, StreamingResponse
from langchain_mcp_adapters.client import MultiServerMCPClient
from langchain_openai import ChatOpenAI
from langgraph.checkpoint.memory import InMemorySaver

AGENT_CONFIG = os.environ.get("AGENT_CONFIG", "/local/agent.yaml")
AGENT_RUNTIME = os.environ.get("AGENT_RUNTIME", "/secrets/runtime.yaml")

# The deep-agent built-in tools, grouped by the YAML `builtins` toggle that
# enables them. Anything not enabled is excluded from the model's tool set. The
# `task` tool (subagent delegation) is bound automatically iff subagents exist,
# so it is not managed here.
BUILTIN_TOOLS = {
    "planning": ["write_todos"],
    "filesystem": ["ls", "read_file", "write_file", "edit_file", "glob", "grep"],
}
ALL_BUILTINS = [name for names in BUILTIN_TOOLS.values() for name in names]

# BASE_SYSTEM_PROMPT is prepended to every agent's own instructions. It sets the
# house rules that hold regardless of what a template author writes — chiefly
# that responses are rendered as Markdown in the Portal chat, so the model should
# emit GitHub-flavored Markdown (headings, **bold**, `code`, fenced code blocks,
# and tables with a `|---|` separator row) rather than plain text.
BASE_SYSTEM_PROMPT = """\
You are an AI agent in the Secured Dev Workspace portal. Your replies are rendered
as GitHub-flavored Markdown in a chat window, so format for readability:

- Use Markdown structure: headings, **bold**, bulleted/numbered lists, and
  `inline code` for identifiers, paths, commands, and values.
- Put multi-line code, config, or command output in fenced code blocks (```).
- Present tabular data as a Markdown table, including the `|---|---|` separator
  row directly under the header so it renders as a real table.
- Be concise and direct. Lead with the answer, then supporting detail.
- Never invent tool results or data. If a tool call fails or returns nothing,
  say so plainly.

Follow the task-specific instructions below."""


class Runtime:
    """Process-wide singletons populated at startup."""

    agent = None
    spec: dict = {}
    tool_names: list = []
    error: str = ""


def _load():
    with open(AGENT_CONFIG) as f:
        spec = yaml.safe_load(f) or {}
    with open(AGENT_RUNTIME) as f:
        runtime = yaml.safe_load(f) or {}
    return spec, runtime


def _make_model(model_name: str, llm: dict) -> ChatOpenAI:
    # Every fronted model is reached through the OpenAI-compatible LiteLLM proxy,
    # so a bare model name always pairs with the shared base_url + per-agent key.
    return ChatOpenAI(
        model=model_name,
        base_url=llm["base_url"],
        api_key=llm["api_key"],
        streaming=True,
    )


async def _build():
    spec, runtime = _load()
    llm = runtime.get("llm", {})

    # MCP tools: one entry per selected server, authenticated with its scoped
    # token. Absent servers → an agent with only its built-in tools.
    servers = {}
    for s in runtime.get("mcp_servers", []) or []:
        servers[s["name"]] = {
            "transport": s.get("transport", "sse"),
            "url": s["url"],
            "headers": {"Authorization": f"Bearer {s['token']}"},
        }
    tools = []
    if servers:
        tools = await MultiServerMCPClient(servers).get_tools()

    # Built-in tools: keep only those the YAML opts into; exclude the rest. The
    # profile is keyed by provider "openai" because the model is a ChatOpenAI.
    enabled = set()
    for name in (spec.get("tools") or {}).get("builtins", []) or []:
        enabled.update(BUILTIN_TOOLS.get(name, []))
    excluded = frozenset(t for t in ALL_BUILTINS if t not in enabled)
    register_harness_profile("openai", HarnessProfile(excluded_tools=excluded))

    subagents = []
    for s in spec.get("subagents") or []:
        entry = {
            "name": s["name"],
            "description": s["description"],
            "system_prompt": s["instructions"],
        }
        if s.get("llm"):
            entry["model"] = _make_model(s["llm"], llm)
        subagents.append(entry)

    agent = create_deep_agent(
        model=_make_model(spec["llm"], llm),
        tools=tools,
        system_prompt=BASE_SYSTEM_PROMPT + "\n\n" + spec["instructions"],
        subagents=subagents or None,
        checkpointer=InMemorySaver(),
    )
    return agent, spec, [t.name for t in tools]


def _sse(event: str, data: dict) -> str:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n"


app = FastAPI()


@app.on_event("startup")
async def _startup():
    try:
        Runtime.agent, Runtime.spec, Runtime.tool_names = await _build()
    except Exception as exc:  # surfaced via /healthz; container stays up to report it
        Runtime.error = str(exc)


@app.get("/healthz")
def healthz():
    if Runtime.agent is None:
        return JSONResponse(
            {"status": "error", "error": Runtime.error or "agent not built"},
            status_code=503,
        )
    return {"status": "ok", "agent": Runtime.spec.get("name"), "tools": Runtime.tool_names}


@app.get("/info")
def info():
    s = Runtime.spec
    return {
        "name": s.get("name"),
        "description": s.get("description"),
        "greeting": s.get("greeting"),
        "model": s.get("llm"),
    }


@app.post("/chat")
async def chat(req: Request):
    if Runtime.agent is None:
        return JSONResponse({"error": Runtime.error or "agent not ready"}, status_code=503)
    body = await req.json()
    message = body.get("message", "")
    thread_id = body.get("thread_id") or "default"

    async def stream():
        config = {"configurable": {"thread_id": thread_id}}
        try:
            async for ev in Runtime.agent.astream_events(
                {"messages": [("user", message)]}, config=config, version="v2"
            ):
                kind = ev["event"]
                if kind == "on_chat_model_stream":
                    token = ev["data"]["chunk"].content
                    if token and isinstance(token, str):
                        yield _sse("token", {"text": token})
                elif kind == "on_tool_start":
                    yield _sse("tool", {"name": ev["name"]})
            yield _sse("done", {})
        except Exception as exc:
            yield _sse("error", {"message": str(exc)})

    return StreamingResponse(stream(), media_type="text/event-stream")
