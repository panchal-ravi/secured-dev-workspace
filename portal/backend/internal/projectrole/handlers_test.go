package projectrole

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// muxWithUser mounts the project-role routes and injects a fixed actor so the
// handlers' actor() call resolves (no auth middleware in the test).
func muxWithUser(h *Handlers, actor string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{name}/roles", h.List)
	mux.HandleFunc("POST /api/projects/{name}/roles", h.Grant)
	mux.HandleFunc("DELETE /api/projects/{name}/roles/{subject}", h.Revoke)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithUser(r.Context(), auth.User{Email: actor}))
		mux.ServeHTTP(w, r)
	})
}

func TestHandlersGrantListRevoke(t *testing.T) {
	st := store.NewMemory()
	h := NewHandlers(New(st))
	srv := httptest.NewServer(muxWithUser(h, "pa@x"))
	defer srv.Close()

	// grant
	body := strings.NewReader(`{"subject":"alice@x","role":"project-admin"}`)
	resp, _ := http.Post(srv.URL+"/api/projects/project-acme/roles", "application/json", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("grant status=%d", resp.StatusCode)
	}

	// list
	resp, _ = http.Get(srv.URL + "/api/projects/project-acme/roles")
	var listed struct {
		Roles []store.ProjectRole `json:"roles"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listed)
	if len(listed.Roles) != 1 || listed.Roles[0].Subject != "alice@x" {
		t.Fatalf("list: %+v", listed.Roles)
	}

	// revoke the only admin → 409
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/projects/project-acme/roles/alice@x", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("revoke last-admin status=%d want 409", resp.StatusCode)
	}
}
