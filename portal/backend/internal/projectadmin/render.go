package projectadmin

import (
	"fmt"
)

var defaultPaths = map[string]string{"sse": "/sse", "streamable-http": "/mcp"}

func jobName(project, name string) string     { return "mcp-" + project + "-" + name }
func serviceName(project, name string) string { return "mcp-" + project + "-" + name }

func projectDiscoveryTags(project string, in DeployInput) []string {
	path := in.Path
	if path == "" {
		path = defaultPaths[in.Transport]
	}
	return []string{
		"mcp",
		"mcp.transport=" + in.Transport,
		"mcp.path=" + path,
		"mcp.auth=none",
		"mcp.scope=project",
		"mcp.project=" + project,
		"mcp.managed-by=portal",
		"mcp.server=" + in.Name,
	}
}

// peerURL builds the ContextForge peer URL from the node IP and the Nomad-assigned
// host port (project-plane MCP jobs use a dynamic host port, so the reachable port
// is not the container port but the value resolved from the placed allocation).
func peerURL(nodeIP string, hostPort int, transport, path string) string {
	if path == "" {
		path = defaultPaths[transport]
	}
	return fmt.Sprintf("http://%s:%d%s", nodeIP, hostPort, path)
}
