package agents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// The template test-chat handler streams the agent's SSE body through unchanged.
func TestChatHandlerPassesThroughSSE(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	nomad := &fakeNomad{jobID: "agent-project-acme-tmpl-support-triage", ip: "10.0.0.5", port: 27000}
	sse := "event: token\ndata: {\"text\":\"hel\"}\n\nevent: tool\ndata: {\"name\":\"search\"}\n\nevent: done\ndata: {}\n\n"
	svc := newService(t, st, nomad, newFakeVault(), newFakeLLM(), newFakeGateway(), &fakeHTTP{resp: bodyResp(200, sse)})
	saveDeployTest(t, svc, goodYAML)

	h := NewHandlers(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{name}/agents/templates/{tmpl}/chat", h.Chat)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/project-acme/agents/templates/support-triage/chat",
		strings.NewReader(`{"message":"hi","thread_id":"t1"}`))
	req = req.WithContext(auth.WithUser(context.Background(), auth.User{Email: "adm@x", Groups: []string{"project-acme-developers"}}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: %s", ct)
	}
	if body := rec.Body.String(); body != sse {
		t.Fatalf("body not passed through verbatim:\n%q", body)
	}
}
