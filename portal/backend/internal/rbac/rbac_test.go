package rbac

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/auth"
)

func TestIsPlatformAdmin(t *testing.T) {
	tests := []struct {
		name   string
		groups []string
		want   bool
	}{
		{"exact", []string{"platform-admins"}, true},
		{"case-insensitive", []string{"Platform-Admins"}, true},
		{"whitespace", []string{"  platform-admins  "}, true},
		{"among others", []string{"acme-developers", "platform-admins"}, true},
		{"not a member", []string{"acme-admins", "acme-developers"}, false},
		{"empty", nil, false},
		{"similar but not equal", []string{"platform-admin"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPlatformAdmin(tt.groups); got != tt.want {
				t.Errorf("IsPlatformAdmin(%v) = %v, want %v", tt.groups, got, tt.want)
			}
		})
	}
}

func TestRolesFor(t *testing.T) {
	if got := RolesFor([]string{"platform-admins"}); len(got) != 1 || got[0] != RolePlatformAdmin {
		t.Errorf("RolesFor(platform-admins) = %v, want [%v]", got, RolePlatformAdmin)
	}
	if got := RolesFor([]string{"acme-developers"}); len(got) != 0 {
		t.Errorf("RolesFor(acme-developers) = %v, want []", got)
	}
}

func TestRequirePlatformAdmin(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := RequirePlatformAdmin(next)

	tests := []struct {
		name     string
		user     *auth.User // nil = no authenticated user in context
		wantCode int
	}{
		{"admin allowed", &auth.User{Email: "a@x", Groups: []string{"platform-admins"}}, http.StatusOK},
		{"non-admin denied", &auth.User{Email: "d@x", Groups: []string{"acme-developers"}}, http.StatusForbidden},
		{"no user denied", nil, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/x", nil)
			if tt.user != nil {
				req = req.WithContext(auth.WithUser(req.Context(), *tt.user))
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}
