// Package logging builds the portal's structured logger. Logs are emitted as
// JSON to stdout (12-factor: the platform — Nomad — captures and ships them), at
// a level taken from PORTAL_LOG_LEVEL. Callers set the returned logger as the
// slog default so every package can log without threading a handle through.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON slog.Logger at the named level (debug|info|warn|error,
// default info). Never log secrets or request bodies through it.
func New(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
