package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// statusRecorder captures the status code and byte count so the access log can
// report them. Defaults to 200 if the handler writes a body without an explicit
// WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Logger emits one structured line per request after it completes: method, path,
// status, duration, byte count, request id, and the authenticated user (if any).
// It never logs query strings, headers, or bodies, so no secrets leak.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestID(r.Context()),
			}
			if u := userOf(r.Context()); u != "" {
				attrs = append(attrs, "user", u)
			}
			log.Info("request", attrs...)
		})
	}
}
