package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover converts a panic in any downstream handler into a logged 500 instead of
// a torn-down connection. The stack trace and request id are logged server-side;
// the client gets only a generic message plus the id. If the response has already
// started, it can only log (the status is already on the wire).
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered",
						"panic", rec,
						"request_id", RequestID(r.Context()),
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					writeJSONError(w, r, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
