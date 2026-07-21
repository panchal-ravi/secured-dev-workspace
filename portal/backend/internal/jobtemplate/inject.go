package jobtemplate

import (
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// Injection markers a base template carries (as bare comments, so they survive both
// render passes untouched). Inject replaces them with the project-admin's add-on
// wiring. Kept in sync with the seed HCL.
const (
	markerSecrets    = "# @project-addons:secrets"
	markerEntrypoint = "# @project-addons:entrypoint"
)

// Inject renders the project-admin's structured add-ons into an already pass-1-baked
// template by replacing the two @project-addons markers. It generates:
//   - secrets marker → Vault secret template{} blocks (MCP url/token + extra-engine
//     KV fields) that consul-template renders to the /secrets tmpfs at launch;
//   - entrypoint marker → per-MCP-server registrations, in the form the workspace's
//     coding agent expects: `claude mcp add` for Claude Code, or a jq-built
//     ~/.bob/mcp_settings.json for IBM Bob Shell.
//
// codingAgent selects the entrypoint form ("bob" → Bob Shell; anything else,
// including "", → Claude Code — the historical default). The secrets block is
// agent-agnostic (both agents read the same /secrets tmpfs url/token files).
//
// The generated content uses only consul-template {{ }} + bash $(...) + literal
// /secrets paths — no ${...} placeholders — so it never disturbs the pass-2 render.
// A missing marker makes that section a no-op (older templates simply carry no
// add-ons region). Deterministic ordering keeps re-renders stable (no needless diffs).
func Inject(baseRendered string, addons store.TemplateAddons, codingAgent string) string {
	// A missing marker makes Replace a no-op (older templates carry no add-ons region).
	out := strings.Replace(baseRendered, markerSecrets, secretsBlock(addons), 1)
	out = strings.Replace(out, markerEntrypoint, entrypointBlock(addons, codingAgent), 1)
	return out
}

// mcpKVPath is the per-server project MCP KV path (KV v2 data path) a workspace reads
// the gateway URL + scoped bearer token from. Written by the add-ons apply step.
func mcpKVPath(server string) string { return "secret/data/projects/mcp/" + server }

// secretsBlock builds the template{} blocks injected at the secrets marker (6-space
// indent, matching the surrounding task blocks).
func secretsBlock(a store.TemplateAddons) string {
	var b strings.Builder
	tmpl := func(dest, path, field string) {
		fmt.Fprintf(&b, "      template {\n")
		fmt.Fprintf(&b, "        destination = %q\n", "secrets/"+dest)
		fmt.Fprintf(&b, "        perms       = \"0644\"\n")
		fmt.Fprintf(&b, "        change_mode = \"noop\"\n")
		fmt.Fprintf(&b, "        data        = <<EOH\n")
		fmt.Fprintf(&b, "{{ with secret %q }}{{ .Data.data.%s }}{{ end }}\n", path, field)
		fmt.Fprintf(&b, "EOH\n      }\n\n")
	}
	for _, name := range a.MCPServers {
		tmpl("mcp-"+name+"-url", mcpKVPath(name), "url")
		tmpl("mcp-"+name+"-token", mcpKVPath(name), "token")
	}
	for _, e := range a.Engines {
		for _, f := range e.SecretFiles {
			tmpl(f.DestFile, e.KVPath, f.KVField)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// entrypointBlock builds the bash lines injected at the entrypoint marker (column 0,
// inside the entrypoint heredoc). MCP servers are registered user-scoped + idempotently;
// a failure WARNs without aborting the workspace. The registration form matches the
// workspace's coding agent (codingAgent=="bob" → Bob Shell; else Claude Code).
func entrypointBlock(a store.TemplateAddons, codingAgent string) string {
	if codingAgent == "bob" {
		return bobEntrypointBlock(a)
	}
	var b strings.Builder
	for _, name := range a.MCPServers {
		fmt.Fprintf(&b, "if ! sudo -u dev claude mcp list 2>/dev/null | grep -q %q; then\n", name)
		fmt.Fprintf(&b, "  sudo -u dev claude mcp add --scope user --transport sse %s \"$(cat /secrets/mcp-%s-url)\" \\\n", name, name)
		fmt.Fprintf(&b, "    --header \"Authorization: Bearer $(cat /secrets/mcp-%s-token)\" \\\n", name)
		fmt.Fprintf(&b, "    || echo \"WARN: failed to register MCP server %s\" >&2\n", name)
		fmt.Fprintf(&b, "fi\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// bobEntrypointBlock registers each MCP server into IBM Bob Shell's global config
// ~/.bob/mcp_settings.json as a remote SSE server (url + Authorization header), using
// jq to merge so multiple servers accumulate and a re-run is idempotent. The url and
// bearer token are read at boot from the /secrets tmpfs files rendered by secretsBlock
// (identical to the Claude path). A jq failure WARNs without aborting the workspace.
//
// Only bash command-substitution `$(...)` and jq's `$u`/`$t` variables appear here —
// never a bare `$$`, which Nomad's HCL parser would collapse to a literal `$` when it
// parses the rendered heredoc. The temp file uses a fixed name (servers register
// sequentially at boot), so no PID is needed.
func bobEntrypointBlock(a store.TemplateAddons) string {
	if len(a.MCPServers) == 0 {
		return ""
	}
	const cfg = "/home/dev/.bob/mcp_settings.json"
	var b strings.Builder
	fmt.Fprintf(&b, "sudo -u dev mkdir -p /home/dev/.bob\n")
	fmt.Fprintf(&b, "[ -f %s ] || echo '{\"mcpServers\":{}}' | sudo -u dev tee %s >/dev/null\n", cfg, cfg)
	for _, name := range a.MCPServers {
		fmt.Fprintf(&b, "if sudo -u dev jq \\\n")
		fmt.Fprintf(&b, "  --arg u \"$(cat /secrets/mcp-%s-url)\" \\\n", name)
		fmt.Fprintf(&b, "  --arg t \"Bearer $(cat /secrets/mcp-%s-token)\" \\\n", name)
		fmt.Fprintf(&b, "  '.mcpServers[%q] = {url:$u, headers:{Authorization:$t}}' \\\n", name)
		fmt.Fprintf(&b, "  %s > /tmp/bob-mcp.json; then\n", cfg)
		fmt.Fprintf(&b, "  sudo -u dev cp /tmp/bob-mcp.json %s\n", cfg)
		fmt.Fprintf(&b, "else\n")
		fmt.Fprintf(&b, "  echo \"WARN: failed to register MCP server %s for bob\" >&2\n", name)
		fmt.Fprintf(&b, "fi\n")
		fmt.Fprintf(&b, "rm -f /tmp/bob-mcp.json\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
