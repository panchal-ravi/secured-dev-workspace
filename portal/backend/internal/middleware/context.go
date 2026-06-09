// Package middleware holds the portal's cross-cutting HTTP middleware: per-request
// context (request id + authenticated user, for log correlation), structured
// access logging, panic recovery, security headers, same-origin CSRF defense, and
// per-user rate limiting. It deliberately does not import the auth package (auth
// imports this one to record the user), so the holder below is how the user email
// reaches the logger and rate limiter without a cycle.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// reqInfo is the mutable per-request holder. A pointer is stored in the context
// by Context() so downstream code (auth.Require → SetUser) can fill in the user
// after the request id is already assigned.
type reqInfo struct {
	id   string
	user string
}

type reqInfoKey struct{}

// Context assigns a request id, stores the holder in the context, and echoes the
// id in the X-Request-Id response header. It must wrap the chain before any
// middleware or handler that calls RequestID/SetUser.
func Context(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := &reqInfo{id: randID()}
		w.Header().Set("X-Request-Id", info.id)
		ctx := context.WithValue(r.Context(), reqInfoKey{}, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestID returns the request id assigned by Context, or "" if absent.
func RequestID(ctx context.Context) string {
	if info, ok := ctx.Value(reqInfoKey{}).(*reqInfo); ok {
		return info.id
	}
	return ""
}

// SetUser records the authenticated user's email on the request holder so the
// access log and rate limiter can see it. No-op if Context did not run.
func SetUser(ctx context.Context, email string) {
	if info, ok := ctx.Value(reqInfoKey{}).(*reqInfo); ok {
		info.user = email
	}
}

// userOf returns the recorded user email, or "".
func userOf(ctx context.Context) string {
	if info, ok := ctx.Value(reqInfoKey{}).(*reqInfo); ok {
		return info.user
	}
	return ""
}

func randID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeJSONError writes the portal's standard error envelope (matching the api
// package) including the request id, so a user can quote it when reporting.
func writeJSONError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":      msg,
		"request_id": RequestID(r.Context()),
	})
}
