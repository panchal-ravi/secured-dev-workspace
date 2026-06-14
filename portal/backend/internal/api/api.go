// Package api exposes the portal's HTTP surface: OIDC auth routes, the
// developer/project/workspace JSON API (behind auth), and the SPA static files.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/admin"
	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
	"github.com/secured-dev-workspace/developer-portal/internal/rbac"
	"github.com/secured-dev-workspace/developer-portal/internal/workspace"
)

type server struct {
	auth  *auth.Authenticator
	svc   *workspace.Service
	ready func(context.Context) error // readiness probe; nil = always ready
}

// Options configures the mux. Ready is the /readyz probe (dependency reachability)
// and RateLimit throttles mutating endpoints per user. Admin is the optional
// Platform Admin onboarding plane; when nil its routes are simply not mounted.
type Options struct {
	Auth      *auth.Authenticator
	Svc       *workspace.Service
	Admin     *admin.Handlers
	StaticDir string
	Ready     func(context.Context) error
	RateLimit middleware.RateLimitConfig
}

// NewMux wires every route and returns the root handler.
func NewMux(opts Options) http.Handler {
	s := &server{auth: opts.Auth, svc: opts.Svc, ready: opts.Ready}
	mux := http.NewServeMux()

	// Liveness/readiness — public, no secrets. /health is the Nomad service check.
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("GET /auth/login", opts.Auth.LoginHandler)
	mux.HandleFunc("GET /auth/callback", opts.Auth.CallbackHandler)
	mux.HandleFunc("POST /auth/logout", opts.Auth.LogoutHandler)

	rl := middleware.NewRateLimiter(opts.RateLimit)
	protect := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(h) }
	// mutate adds per-user rate limiting on top of auth for state-changing routes.
	mutate := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(rl.Wrap(http.HandlerFunc(h))) }

	mux.Handle("GET /api/me", protect(s.me))
	mux.Handle("GET /api/workspaces", protect(s.listAllWorkspaces))
	mux.Handle("GET /api/projects", protect(s.listProjects))
	mux.Handle("GET /api/projects/{name}/workspaces", protect(s.listWorkspaces))
	mux.Handle("POST /api/projects/{name}/workspaces", mutate(s.createWorkspace))
	mux.Handle("POST /api/projects/{name}/workspaces/{ws}/stop", mutate(s.stopWorkspace))
	mux.Handle("POST /api/projects/{name}/workspaces/{ws}/start", mutate(s.startWorkspace))
	mux.Handle("GET /api/projects/{name}/workspaces/{ws}/logs", protect(s.workspaceLogs))
	mux.Handle("POST /api/projects/{name}/workspaces/{ws}/ssh-config", mutate(s.writeSSHConfig))
	mux.Handle("DELETE /api/projects/{name}/workspaces/{ws}", mutate(s.destroyWorkspace))

	// Platform Admin onboarding plane (optional). Every route is gated by
	// auth.Require + rbac.RequirePlatformAdmin; mutations also rate-limit per user.
	if opts.Admin != nil {
		adminProtect := func(h http.HandlerFunc) http.Handler {
			return opts.Auth.Require(rbac.RequirePlatformAdmin(http.HandlerFunc(h)))
		}
		adminMutate := func(h http.HandlerFunc) http.Handler {
			return opts.Auth.Require(rbac.RequirePlatformAdmin(rl.Wrap(http.HandlerFunc(h))))
		}
		opts.Admin.Register(mux, adminProtect, adminMutate)
	}

	// The secured-ws:// helper download. Served from a sibling of the SPA dir so the
	// frontend build (which empties ./web) never deletes it. Public, no secrets.
	helperDir := filepath.Join(filepath.Dir(opts.StaticDir), "helper-dist")
	mux.Handle("GET /helper/", http.StripPrefix("/helper/", http.FileServer(http.Dir(helperDir))))

	mux.Handle("/", spaHandler(opts.StaticDir))
	return mux
}

// ---- DTOs ----

type flavorDTO struct {
	Name        string               `json:"name"`
	Label       string               `json:"label,omitempty"`
	Description string               `json:"description,omitempty"`
	GitRepoURL  string               `json:"git_repo_url,omitempty"`
	Image       string               `json:"image,omitempty"`
	NodePool    string               `json:"node_pool,omitempty"`
	Features    []descriptor.Feature `json:"features"`
}

type projectDTO struct {
	Name      string      `json:"name"`
	Namespace string      `json:"namespace"`
	Flavors   []flavorDTO `json:"flavors"`
}

func toProjectDTO(d descriptor.Descriptor) projectDTO {
	fl := make([]flavorDTO, 0, len(d.Flavors))
	for _, f := range d.Flavors {
		fl = append(fl, flavorDTO{
			Name:        f.Name,
			Label:       f.Label,
			Description: f.Description,
			GitRepoURL:  f.GitRepoURL,
			Image:       f.Image,
			NodePool:    f.NodePool,
			Features:    f.Features,
		})
	}
	return projectDTO{Name: d.ProjectName, Namespace: d.Namespace, Flavors: fl}
}

// ---- Handlers ----

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"email":     u.Email,
		"handle":    u.Handle,
		"groups":    u.Groups,
		"roles":     rbac.RolesFor(u.Groups),
		"local_ssh": s.svc.LocalSSHEnabled(),
	})
}

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	ds, err := s.svc.ListProjects(r.Context(), u.Groups)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]projectDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, toProjectDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *server) listAllWorkspaces(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	wss, err := s.svc.ListAllWorkspaces(r.Context(), u.Groups, u.Handle)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": wss})
}

func (s *server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	d, err := s.svc.GetProject(r.Context(), r.PathValue("name"), u.Groups)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	wss, err := s.svc.ListWorkspaces(r.Context(), d, u.Handle)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProjectDTO(d), "workspaces": wss})
}

func (s *server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	d, err := s.svc.GetProject(r.Context(), r.PathValue("name"), u.Groups)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var body struct {
		Flavor string `json:"flavor"`
	}
	// The only field is flavor, optional for single-flavor projects, so an empty
	// body is valid; reject only malformed JSON.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	ws, err := s.svc.Create(r.Context(), d, workspace.CreateInput{
		Flavor:  body.Flavor,
		Email:   u.Email,
		Handle:  u.Handle,
		GitName: u.GitName,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

// lifecycleAction resolves the project (authz by groups) and runs fn against the
// {ws} job, returning 204 on success. Shared by stop/start/destroy.
func (s *server) lifecycleAction(w http.ResponseWriter, r *http.Request, fn func(d descriptor.Descriptor, handle, ws string) error) {
	u, _ := auth.UserFrom(r.Context())
	d, err := s.svc.GetProject(r.Context(), r.PathValue("name"), u.Groups)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := fn(d, u.Handle, r.PathValue("ws")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) stopWorkspace(w http.ResponseWriter, r *http.Request) {
	s.lifecycleAction(w, r, func(d descriptor.Descriptor, handle, ws string) error {
		return s.svc.Stop(r.Context(), d, handle, ws)
	})
}

func (s *server) startWorkspace(w http.ResponseWriter, r *http.Request) {
	s.lifecycleAction(w, r, func(d descriptor.Descriptor, handle, ws string) error {
		return s.svc.Start(r.Context(), d, handle, ws)
	})
}

func (s *server) destroyWorkspace(w http.ResponseWriter, r *http.Request) {
	s.lifecycleAction(w, r, func(d descriptor.Descriptor, handle, ws string) error {
		return s.svc.Destroy(r.Context(), d, handle, ws)
	})
}

func (s *server) workspaceLogs(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	d, err := s.svc.GetProject(r.Context(), r.PathValue("name"), u.Groups)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	logType := r.URL.Query().Get("type")
	if logType == "" {
		logType = "stdout"
	}
	out, err := s.svc.Logs(r.Context(), d, u.Handle, r.PathValue("ws"), logType)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": out})
}

func (s *server) writeSSHConfig(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	d, err := s.svc.GetProject(r.Context(), r.PathValue("name"), u.Groups)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	host, err := s.svc.WriteSSHConfig(r.Context(), d, u.Handle, r.PathValue("ws"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"host": host})
}

// ---- health ----

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.ready != nil {
		if err := s.ready(r.Context()); err != nil {
			slog.Warn("readiness check failed", "err", err, "request_id", middleware.RequestID(r.Context()))
			writeErr(w, http.StatusServiceUnavailable, "not ready", middleware.RequestID(r.Context()))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// ---- helpers ----

// fail classifies err and responds. Known client-error classes (apperr) return
// their safe message and status; anything else is an internal/upstream failure —
// the detail is logged with the request id and the client gets only a generic
// 502 so backend errors never leak (addresses, tokens, internal wording).
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if status := apperr.Status(err); status != 0 {
		writeErr(w, status, err.Error(), rid)
		return
	}
	slog.Error("request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
	writeErr(w, http.StatusBadGateway, "upstream service error", rid)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg, requestID string) {
	writeJSON(w, status, map[string]string{"error": msg, "request_id": requestID})
}

// spaHandler serves static files from dir, falling back to index.html for
// client-side routes (any non-/api, non-/auth path that isn't a real file).
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			http.NotFound(w, r)
			return
		}
		clean := filepath.Clean(r.URL.Path)
		if _, err := os.Stat(filepath.Join(dir, clean)); err == nil && clean != "/" {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
