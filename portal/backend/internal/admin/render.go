package admin

import (
	"fmt"

	"github.com/secured-dev-workspace/developer-portal/internal/mcpjob"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// transport → default MCP path on the server.
var defaultPaths = map[string]string{
	"sse":             "/sse",
	"streamable-http": "/mcp",
}

// renderMCPJobHCL builds the platform-deployed MCP server job by delegating to the
// neutral mcpjob renderer with a KV-secret_refs credential. The shape (single mcp
// group, static host port, discovery tags, Vault-WIF secret template) is unchanged.
func renderMCPJobHCL(s store.MCPServer, cfg Config) string {
	var cred mcpjob.Credential
	if len(s.SecretRefs) > 0 {
		cred = mcpjob.KVCredential{VaultRole: cfg.MCPJobVaultRole, SecretRefs: s.SecretRefs}
	}
	return mcpjob.Render(mcpjob.RenderSpec{
		JobName:     jobName(s.Name),
		Namespace:   cfg.MCPNamespace,
		NodePool:    cfg.NodePool,
		Image:       s.Image,
		Command:     s.Command,
		Port:        s.Port,
		Env:         s.Env,
		ServiceName: serviceName(s.Name),
		Tags:        discoveryTags(s),
		Credential:  cred,
	})
}

// discoveryTags returns the Nomad service tags the portal and gateway filter on.
// auth=none in this slice (no wrapper); managed-by=portal marks portal-deployed.
func discoveryTags(s store.MCPServer) []string {
	path := s.Path
	if path == "" {
		path = defaultPaths[s.Transport]
	}
	return []string{
		"mcp",
		"mcp.transport=" + s.Transport,
		"mcp.path=" + path,
		"mcp.auth=none",
		"mcp.scope=platform",
		"mcp.managed-by=portal",
		"mcp.server=" + s.Name,
	}
}

// peerURL builds the ContextForge peer URL for a deployed server on nodeIP.
func peerURL(nodeIP string, s store.MCPServer) string {
	path := s.Path
	if path == "" {
		path = defaultPaths[s.Transport]
	}
	return fmt.Sprintf("http://%s:%d%s", nodeIP, s.Port, path)
}

func jobName(name string) string     { return "mcp-" + name }
func serviceName(name string) string { return "mcp-" + name }
