package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The MCP-server and blueprint planes moved to project-admins (internal/
// projectadmin). Their admin routes must be GONE — not just unauthorized.
func TestRegister_MCPAndBlueprintRoutesGone(t *testing.T) {
	svc, _ := newService(&fakeLLM{}, &fakeVault{provider: map[string]string{}})
	mux := http.NewServeMux()
	pass := func(h http.HandlerFunc) http.Handler { return h }
	NewHandlers(svc).Register(mux, pass, pass)

	gone := []struct{ method, path string }{
		{http.MethodGet, "/api/admin/mcp-servers"},
		{http.MethodGet, "/api/admin/mcp-servers/published"},
		{http.MethodPost, "/api/admin/mcp-servers"},
		{http.MethodPost, "/api/admin/mcp-servers/x/test"},
		{http.MethodPost, "/api/admin/mcp-servers/x/publish"},
		{http.MethodDelete, "/api/admin/mcp-servers/x"},
		{http.MethodGet, "/api/admin/blueprints"},
		{http.MethodPost, "/api/admin/blueprints"},
		{http.MethodPost, "/api/admin/blueprints/x/1/validate"},
		{http.MethodPost, "/api/admin/blueprints/x/1/publish"},
		{http.MethodDelete, "/api/admin/blueprints/x/1"},
	}
	for _, g := range gone {
		if _, pattern := mux.Handler(httptest.NewRequest(g.method, g.path, nil)); pattern != "" {
			t.Fatalf("%s %s must not be routed anymore (matched %q)", g.method, g.path, pattern)
		}
	}
	// The LLM + audit plane stays.
	for _, keep := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/llm/models"},
		{http.MethodGet, "/api/admin/audit"},
	} {
		if _, pattern := mux.Handler(httptest.NewRequest(keep.method, keep.path, nil)); pattern == "" {
			t.Fatalf("%s %s must remain routed", keep.method, keep.path)
		}
	}
}
