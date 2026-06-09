// Package auth implements the portal's IBM Verify OIDC login (Authorization Code
// + PKCE) and a cookie session carrying the developer's identity and groups.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"

	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
)

const (
	sessionName = "portal_session"
	flowName    = "portal_flow" // short-lived: OIDC state + PKCE verifier
)

// User is the authenticated developer derived from the ID token.
type User struct {
	Email   string   `json:"email"`
	Handle  string   `json:"handle"`
	GitName string   `json:"git_name"`
	Groups  []string `json:"groups"`
}

// flexStrings unmarshals a JSON value that is either a single string or an
// array of strings. IBM Verify emits a multi-valued claim such as groups as a
// bare string when the user has exactly one value and as an array otherwise, so
// the claim decode must accept both shapes.
type flexStrings []string

func (s *flexStrings) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	if one == "" {
		*s = nil
		return nil
	}
	*s = []string{one}
	return nil
}

type ctxKey struct{}

// Authenticator wires the OIDC provider, OAuth2 config, and session store.
type Authenticator struct {
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	store    *sessions.CookieStore
	secure   bool
}

// New discovers the issuer and builds the authenticator. secureCookies should be
// false for the local http://localhost PoC and true behind TLS.
func New(ctx context.Context, issuer, clientID, clientSecret, redirectURL, sessionSecret string, secureCookies bool) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: discover issuer: %w", err)
	}
	store := sessions.NewCookieStore([]byte(sessionSecret))
	store.Options = &sessions.Options{Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureCookies, MaxAge: 8 * 3600}
	return &Authenticator{
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		oauth: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "groups", "profile"},
		},
		store:  store,
		secure: secureCookies,
	}, nil
}

// LoginHandler starts the Authorization Code + PKCE flow.
func (a *Authenticator) LoginHandler(w http.ResponseWriter, r *http.Request) {
	state := randString()
	verifier := oauth2.GenerateVerifier()

	flow, _ := a.store.Get(r, flowName)
	flow.Values["state"] = state
	flow.Values["verifier"] = verifier
	flow.Options = &sessions.Options{Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secure, MaxAge: 600}
	if err := flow.Save(r, w); err != nil {
		httpError(w, r, http.StatusInternalServerError, "session error", err)
		return
	}
	// prompt=login forces IBM Verify to re-authenticate (ask for credentials)
	// rather than silently reusing its own SSO session — so a portal sign-out is
	// followed by a real re-login, not a transparent bounce-through.
	url := a.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "login"))
	http.Redirect(w, r, url, http.StatusFound)
}

// CallbackHandler completes the flow, verifies the ID token, and stores the user.
func (a *Authenticator) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	flow, _ := a.store.Get(r, flowName)
	wantState, _ := flow.Values["state"].(string)
	verifier, _ := flow.Values["verifier"].(string)
	if wantState == "" || r.URL.Query().Get("state") != wantState {
		httpError(w, r, http.StatusBadRequest, "invalid state", nil)
		return
	}

	// On any failure below, return a generic "authentication failed" to the client
	// and log the real cause server-side — the upstream errors can carry token or
	// client-secret detail that must never reach the browser.
	oauth2Token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		httpError(w, r, http.StatusBadGateway, "authentication failed", err)
		return
	}
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		httpError(w, r, http.StatusBadGateway, "authentication failed", nil)
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		httpError(w, r, http.StatusUnauthorized, "authentication failed", err)
		return
	}

	var claims struct {
		Email  string      `json:"email"`
		Name   string      `json:"name"`
		Given  string      `json:"given_name"`
		Groups flexStrings `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		httpError(w, r, http.StatusBadGateway, "authentication failed", err)
		return
	}
	if claims.Email == "" {
		httpError(w, r, http.StatusUnauthorized, "no email in identity", nil)
		return
	}

	handle, gitName := DeriveIdentity(claims.Email, firstNonEmpty(claims.Name, claims.Given))
	u := User{Email: claims.Email, Handle: handle, GitName: gitName, Groups: []string(claims.Groups)}

	sess, _ := a.store.Get(r, sessionName)
	b, _ := json.Marshal(u)
	sess.Values["user"] = string(b)
	if err := sess.Save(r, w); err != nil {
		httpError(w, r, http.StatusInternalServerError, "session error", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// LogoutHandler clears the session.
func (a *Authenticator) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	sess, _ := a.store.Get(r, sessionName)
	sess.Options.MaxAge = -1
	_ = sess.Save(r, w)
	w.WriteHeader(http.StatusNoContent)
}

// Require is middleware that injects the user into the context, or 401s. It also
// records the user's email on the request holder so the access log and rate
// limiter (which must not import auth) can attribute the request.
func (a *Authenticator) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.user(r)
		if !ok {
			httpError(w, r, http.StatusUnauthorized, "unauthorized", nil)
			return
		}
		middleware.SetUser(r.Context(), u.Email)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

// httpError writes the portal's JSON error envelope (with the request id) and
// logs the real cause server-side for non-client errors. clientMsg is the only
// detail the browser sees, so it never carries upstream/secret information.
func httpError(w http.ResponseWriter, r *http.Request, status int, clientMsg string, err error) {
	rid := middleware.RequestID(r.Context())
	if err != nil {
		slog.Error("auth", "msg", clientMsg, "err", err, "request_id", rid, "path", r.URL.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": clientMsg, "request_id": rid})
}

func (a *Authenticator) user(r *http.Request) (User, bool) {
	sess, err := a.store.Get(r, sessionName)
	if err != nil {
		return User{}, false
	}
	raw, ok := sess.Values["user"].(string)
	if !ok || raw == "" {
		return User{}, false
	}
	var u User
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return User{}, false
	}
	return u, true
}

// UserFrom extracts the authenticated user injected by Require.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

func randString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
