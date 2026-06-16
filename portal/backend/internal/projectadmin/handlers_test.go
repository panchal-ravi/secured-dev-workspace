package projectadmin

import (
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
	manifestJSON, hash := classAManifestJSON(t)
	ex := &fakeExecutor{rec: blueprintInstance()}
	svc, st := newService(t, ex, &fakeNomad{}, &fakeGateway{}, manifestJSON)
	seedDeployable(t, st, hash)
	h := NewHandlers(svc)

	r := req(http.MethodPost, "/api/projects/project-acme/mcp-servers",
		`{"server_type":"postgres-mcp","params":{"connection_url":"x","bootstrap_password":"b","db_host":"demo-db","db_port":"5432","db_name":"app"}}`,
		auth.User{Email: "acme-admin@x", Groups: []string{"project-acme-developers"}})
	r.SetPathValue("name", "project-acme")
	w := httptest.NewRecorder()
	h.Deploy(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy status %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlersList(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	svc, st := newService(t, &fakeExecutor{}, &fakeNomad{}, &fakeGateway{}, manifestJSON)
	seedDeployable(t, st, hash)
	h := NewHandlers(svc)

	r := req(http.MethodGet, "/api/projects/project-acme/mcp-servers", "", auth.User{Email: "acme-admin@x", Groups: []string{"project-acme-developers"}})
	r.SetPathValue("name", "project-acme")
	w := httptest.NewRecorder()
	h.List(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
}
