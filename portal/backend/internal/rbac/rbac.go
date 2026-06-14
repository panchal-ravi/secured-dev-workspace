// Package rbac maps IBM Verify group memberships to coarse portal roles and
// provides middleware that gates the platform-admin routes. It is the single
// place that knows which Verify group grants which role, so handlers stay free
// of group-name string matching.
package rbac

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
)

// Role is a coarse portal role derived from a user's IBM Verify groups.
type Role string

const (
	// RolePlatformAdmin can onboard MCP servers and LLM models platform-wide.
	RolePlatformAdmin Role = "platform-admin"
)

// platformAdminsGroup is the IBM Verify group that grants RolePlatformAdmin.
// Matched case-insensitively, consistent with group handling across the stack.
const platformAdminsGroup = "platform-admins"

// RolesFor derives the portal roles a user holds from their group memberships.
// Returns an empty (non-nil) slice when the user holds no special role.
func RolesFor(groups []string) []Role {
	roles := []Role{}
	if IsPlatformAdmin(groups) {
		roles = append(roles, RolePlatformAdmin)
	}
	return roles
}

// IsPlatformAdmin reports whether the groups grant the platform-admin role.
func IsPlatformAdmin(groups []string) bool {
	return hasGroup(groups, platformAdminsGroup)
}

func hasGroup(groups []string, want string) bool {
	for _, g := range groups {
		if strings.EqualFold(strings.TrimSpace(g), want) {
			return true
		}
	}
	return false
}

// RequirePlatformAdmin gates a handler to platform admins. It must run inside
// auth.Require (which injects the user); a missing or non-admin user gets 403 in
// the portal's JSON error envelope. Default-deny: any uncertainty is a 403.
func RequirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok || !IsPlatformAdmin(u.Groups) {
			writeForbidden(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeForbidden(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":      "forbidden",
		"request_id": middleware.RequestID(r.Context()),
	})
}
