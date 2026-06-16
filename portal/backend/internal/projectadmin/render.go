package projectadmin

import (
	"fmt"

	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

var defaultPaths = map[string]string{"sse": "/sse", "streamable-http": "/mcp"}

func jobName(project, name string) string     { return "mcp-" + project + "-" + name }
func serviceName(project, name string) string { return "mcp-" + project + "-" + name }

func projectDiscoveryTags(project string, t store.MCPServer) []string {
	path := t.Path
	if path == "" {
		path = defaultPaths[t.Transport]
	}
	return []string{
		"mcp",
		"mcp.transport=" + t.Transport,
		"mcp.path=" + path,
		"mcp.auth=none",
		"mcp.scope=project",
		"mcp.project=" + project,
		"mcp.managed-by=portal",
		"mcp.server=" + t.Name,
	}
}

func peerURL(nodeIP string, t store.MCPServer) string {
	path := t.Path
	if path == "" {
		path = defaultPaths[t.Transport]
	}
	return fmt.Sprintf("http://%s:%d%s", nodeIP, t.Port, path)
}
