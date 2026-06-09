package middleware

import (
	"net/http"
	"strings"
)

// SecurityHeaders sets defensive response headers on every response. The CSP is
// tuned for the Carbon/Vite single-page app: scripts and styles are same-origin
// (Carbon injects some inline styles, hence 'unsafe-inline' for style only),
// fonts/images may be inlined as data: URIs, and the app only talks to its own
// origin. HSTS is sent only when the portal terminates TLS itself.
func SecurityHeaders(tls bool) func(http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"frame-ancestors 'none'"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", csp)
			if tls {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRF defends state-changing requests (POST/PUT/PATCH/DELETE) against cross-site
// forgery using the Fetch Metadata / Origin signals modern browsers always send,
// layered on the SameSite=Lax session cookie. When the browser declares the
// request cross-site (Sec-Fetch-Site) or sends a mismatched Origin, it is
// rejected. Non-browser clients (no Sec-Fetch-Site and no Origin) carry no
// ambient cookie-driven CSRF risk and are allowed, which keeps curl/CLI usable.
// A double-submit token (which would require a frontend change) is the next step.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
				if site != "same-origin" && site != "none" {
					writeJSONError(w, r, http.StatusForbidden, "cross-site request blocked")
					return
				}
			} else if origin := r.Header.Get("Origin"); origin != "" {
				if !originMatchesHost(origin, r.Host) {
					writeJSONError(w, r, http.StatusForbidden, "cross-site request blocked")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// originMatchesHost reports whether the Origin header's host[:port] equals the
// request Host. Origin is "scheme://host[:port]"; compare the authority only.
func originMatchesHost(origin, host string) bool {
	if i := strings.Index(origin, "://"); i >= 0 {
		origin = origin[i+3:]
	}
	return origin == host
}
