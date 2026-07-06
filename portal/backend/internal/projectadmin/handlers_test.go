package projectadmin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/auth"
)

func req(method, path, body string, user auth.User) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(auth.WithUser(r.Context(), user))
	return r
}

func TestHandlersDeploy(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	svc, _ := newService(t, ex, &fakeNomad{}, &fakeGateway{})
	h := NewHandlers(svc)

	body, _ := json.Marshal(pgInput())
	r := req(http.MethodPost, "/api/projects/project-acme/mcp-servers", string(body),
		auth.User{Email: "acme-admin@x", Groups: []string{"project-acme-developers"}})
	r.SetPathValue("name", "project-acme")
	w := httptest.NewRecorder()
	h.Deploy(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy status %d: %s", w.Code, w.Body.String())
	}
	// The secret param value must not appear in the response body (the row
	// serialization is what the UI sees).
	if strings.Contains(w.Body.String(), `"boot"`) {
		t.Fatalf("secret param leaked into deploy response: %s", w.Body.String())
	}
}

func TestHandlersList(t *testing.T) {
	svc, _ := newService(t, &fakeExecutor{}, &fakeNomad{}, &fakeGateway{})
	h := NewHandlers(svc)

	r := req(http.MethodGet, "/api/projects/project-acme/mcp-servers", "", auth.User{Email: "acme-admin@x", Groups: []string{"project-acme-developers"}})
	r.SetPathValue("name", "project-acme")
	w := httptest.NewRecorder()
	h.List(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	// The historical "deployed" key must survive (Templates.tsx consumes it).
	if !strings.Contains(w.Body.String(), `"deployed"`) {
		t.Fatalf("list response must keep the deployed key: %s", w.Body.String())
	}
}
