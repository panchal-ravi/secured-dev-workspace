// Package apperr defines the small set of error classes the API layer maps to
// HTTP status codes. Service-layer code wraps its errors with one of these
// sentinels (fmt.Errorf("...: %w", apperr.ErrForbidden)); the api layer then
// classifies with errors.Is instead of matching on message text. An error not
// wrapped with any sentinel is treated as an internal/upstream failure (5xx),
// whose detail is logged server-side but never returned to the client.
package apperr

import "errors"

// The error classes. Wrap, don't compare to these directly outside Status.
var (
	ErrBadRequest = errors.New("bad request") // 400 — malformed or invalid input
	ErrForbidden  = errors.New("forbidden")   // 403 — authenticated but not allowed
	ErrNotFound   = errors.New("not found")   // 404 — resource does not exist
	ErrConflict   = errors.New("conflict")    // 409 — name/state collision
)

// Status returns the HTTP status for a wrapped error, or 0 when the error is not
// one of the known client-error classes (the caller treats 0 as a 5xx).
func Status(err error) int {
	switch {
	case errors.Is(err, ErrBadRequest):
		return 400
	case errors.Is(err, ErrForbidden):
		return 403
	case errors.Is(err, ErrNotFound):
		return 404
	case errors.Is(err, ErrConflict):
		return 409
	default:
		return 0
	}
}
