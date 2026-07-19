// Package api exposes the portal's HTTP surface: OIDC auth routes, the
// developer/project/workspace JSON API (behind auth), and the SPA static files.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/admin"
	"github.com/secured-dev-workspace/developer-portal/internal/agents"
	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/basetmpladmin"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
	"github.com/secured-dev-workspace/developer-portal/internal/projectadmin"
	"github.com/secured-dev-workspace/developer-portal/internal/projectbootstrap"
	"github.com/secured-dev-workspace/developer-portal/internal/projectengines"
	"github.com/secured-dev-workspace/developer-portal/internal/projectrole"
	"github.com/secured-dev-workspace/developer-portal/internal/projecttemplate"
	"github.com/secured-dev-workspace/developer-portal/internal/rbac"
	"github.com/secured-dev-workspace/developer-portal/internal/sharedvolume"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
	"github.com/secured-dev-workspace/developer-portal/internal/workspace"
)

type server struct {
	auth  *auth.Authenticator
	svc   *workspace.Service
	roles *projectrole.Service
	caps  rbac.CapabilityStore        // nil when no store is wired
	ready func(context.Context) error // readiness probe; nil = always ready
}

// capStore adapts store.Store to rbac.CapabilityStore so the rbac package needs
// no store import.
type capStore struct{ st store.Store }

func (c capStore) RolesForSubjectInProject(ctx context.Context, project, subject string) ([]string, error) {
	grants, err := c.st.ProjectRolesForSubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	roles := []string{}
	for _, g := range grants {
		if g.Project == project {
			roles = append(roles, g.Role)
		}
	}
	return roles, nil
}

func (c capStore) GetCapabilityMatrix(ctx context.Context, project string) (map[string][]string, bool, error) {
	pc, err := c.st.GetProjectCapabilities(ctx, project)
	if errors.Is(err, apperr.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return pc.Matrix, true, nil
}

// Options configures the mux. Ready is the /readyz probe (dependency reachability)
// and RateLimit throttles mutating endpoints per user. Admin is the optional
// Platform Admin onboarding plane; when nil its routes are simply not mounted.
type Options struct {
	Auth          *auth.Authenticator
	Svc           *workspace.Service
	Admin         *admin.Handlers
	BaseTmpl      *basetmpladmin.Handlers    // optional; platform-admin base job-template plane
	ProjectCreate *projectbootstrap.Handlers // optional; platform-admin project-create plane
	ProjectRoles  *projectrole.Service       // optional; project-role plane
	ProjectMCP    *projectadmin.Handlers     // optional; project MCP-deploy plane
	ProjectTmpl   *projecttemplate.Handlers  // optional; project-template create plane
	ProjectEng    *projectengines.Handlers   // optional; project engine-provision plane
	SharedVolumes *sharedvolume.Handlers     // optional; per-project shared EFS volumes
	Agents        *agents.Handlers           // optional; project AI-agents plane (ai-agents capability)
	Store         store.Store                // nil only in tests; backs role + capability gating
	StaticDir     string
	Ready         func(context.Context) error
	RateLimit     middleware.RateLimitConfig
}

// NewMux wires every route and returns the root handler.
func NewMux(opts Options) http.Handler {
	s := &server{auth: opts.Auth, svc: opts.Svc, roles: opts.ProjectRoles, ready: opts.Ready}
	mux := http.NewServeMux()

	// Project guard: role elevation checks (project-admin planes) and the
	// role→capability matrix (workspace/agent actions). Built once; nil without a
	// store, in which case capability gating is off (wsGate stays a passthrough).
	var guard *rbac.Guard
	wsGate := func(h http.Handler) http.Handler { return h }
	if opts.Store != nil {
		member := func(ctx context.Context, project string, groups []string) error {
			_, err := opts.Svc.GetProject(ctx, project, groups)
			return err
		}
		s.caps = capStore{st: opts.Store}
		guard = rbac.NewGuard(opts.Store, s.caps, member)
		wsGate = guard.RequireCapability(rbac.CapWorkspaces)
	}

	// Liveness/readiness — public, no secrets. /health is the Nomad service check.
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("GET /auth/login", opts.Auth.LoginHandler)
	mux.HandleFunc("GET /auth/callback", opts.Auth.CallbackHandler)
	mux.HandleFunc("POST /auth/logout", opts.Auth.LogoutHandler)

	rl := middleware.NewRateLimiter(opts.RateLimit)
	protect := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(h) }
	// wsProtect/wsMutate gate workspace actions on the workspaces capability under
	// the project's role→capability matrix (wsMutate also rate-limits per user).
	// Listing routes stay membership-only — capability governs doing, not seeing.
	wsProtect := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(wsGate(http.HandlerFunc(h))) }
	wsMutate := func(h http.HandlerFunc) http.Handler {
		return opts.Auth.Require(wsGate(rl.Wrap(http.HandlerFunc(h))))
	}

	mux.Handle("GET /api/me", protect(s.me))
	mux.Handle("GET /api/workspaces", protect(s.listAllWorkspaces))
	mux.Handle("GET /api/projects", protect(s.listProjects))
	mux.Handle("GET /api/projects/{name}/workspaces", protect(s.listWorkspaces))
	mux.Handle("POST /api/projects/{name}/workspaces", wsMutate(s.createWorkspace))
	mux.Handle("POST /api/projects/{name}/workspaces/{ws}/stop", wsMutate(s.stopWorkspace))
	mux.Handle("POST /api/projects/{name}/workspaces/{ws}/start", wsMutate(s.startWorkspace))
	mux.Handle("GET /api/projects/{name}/workspaces/{ws}/logs", wsProtect(s.workspaceLogs))
	mux.Handle("POST /api/projects/{name}/workspaces/{ws}/ssh-config", wsMutate(s.writeSSHConfig))
	mux.Handle("DELETE /api/projects/{name}/workspaces/{ws}", wsMutate(s.destroyWorkspace))

	// Project-user AI-agent instance plane (optional): browse published cards and
	// spin up an isolated per-user instance. Gated on the ai-agents capability
	// under the project's role→capability matrix (mutate also rate-limits); a
	// member without the capability 403s. The project-admin template plane that
	// authors these cards registers separately, under the project-admin gate.
	agentGate := func(h http.Handler) http.Handler { return h }
	if guard != nil {
		agentGate = guard.RequireCapability(rbac.CapAIAgents)
	}
	if opts.Agents != nil {
		agentProtect := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(agentGate(http.HandlerFunc(h))) }
		agentMutate := func(h http.HandlerFunc) http.Handler {
			return opts.Auth.Require(agentGate(rl.Wrap(http.HandlerFunc(h))))
		}
		opts.Agents.RegisterInstances(mux, agentProtect, agentMutate)
	}

	// Platform Admin onboarding plane (optional). Every route is gated by
	// auth.Require + rbac.RequirePlatformAdmin; mutations also rate-limit per user.
	adminProtect := func(h http.HandlerFunc) http.Handler {
		return opts.Auth.Require(rbac.RequirePlatformAdmin(http.HandlerFunc(h)))
	}
	adminMutate := func(h http.HandlerFunc) http.Handler {
		return opts.Auth.Require(rbac.RequirePlatformAdmin(rl.Wrap(http.HandlerFunc(h))))
	}
	if opts.Admin != nil {
		opts.Admin.Register(mux, adminProtect, adminMutate)
	}
	// Base job-template plane — independent of the MCP/LLM admin plane (needs no
	// gateway), so it registers on its own whenever wired.
	if opts.BaseTmpl != nil {
		opts.BaseTmpl.Register(mux, adminProtect, adminMutate)
	}
	if opts.ProjectCreate != nil {
		opts.ProjectCreate.Register(mux, adminProtect, adminMutate)
	}

	// Project-role plane (optional): self-service grant/revoke gated on project-admin
	// of the {name} project; platform-admins bootstrap the first admin.
	if opts.ProjectRoles != nil && guard != nil {
		prh := projectrole.NewHandlers(opts.ProjectRoles)
		reqPA := guard.RequireProjectRole(rbac.RoleProjectAdmin)
		paProtect := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(reqPA(http.HandlerFunc(h))) }
		paMutate := func(h http.HandlerFunc) http.Handler { return opts.Auth.Require(reqPA(rl.Wrap(http.HandlerFunc(h)))) }

		mux.Handle("GET /api/projects/{name}/roles", paProtect(prh.List))
		mux.Handle("POST /api/projects/{name}/roles", paMutate(prh.Grant))
		mux.Handle("DELETE /api/projects/{name}/roles/{subject}", paMutate(prh.Revoke))
		mux.Handle("POST /api/admin/projects/{name}/roles", adminMutate(prh.Grant)) // platform-admin bootstrap
		mux.Handle("GET /api/projects/{name}/capabilities", paProtect(prh.GetCapabilities))
		mux.Handle("PUT /api/projects/{name}/capabilities", paMutate(prh.PutCapabilities))

		if opts.ProjectMCP != nil {
			opts.ProjectMCP.Register(mux, paProtect, paMutate)
		}
		if opts.ProjectTmpl != nil {
			opts.ProjectTmpl.Register(mux, paProtect, paMutate)
		}
		if opts.ProjectEng != nil {
			opts.ProjectEng.Register(mux, paProtect, paMutate)
		}
		// Shared volumes: listing is membership-only (a developer populates the
		// workspace-create selection), create/delete are project-admin powers.
		if opts.SharedVolumes != nil {
			mux.Handle("GET /api/projects/{name}/shared-volumes", protect(opts.SharedVolumes.List))
			mux.Handle("POST /api/projects/{name}/shared-volumes", paMutate(opts.SharedVolumes.Create))
			mux.Handle("DELETE /api/projects/{name}/shared-volumes/{vol}", paMutate(opts.SharedVolumes.Delete))
		}
		// Agent TEMPLATE plane: project-admins author/deploy-test/publish agent cards.
		// Authoring is a project-admin power (the sole author of what agents exist);
		// project-users only instantiate published cards (the Phase 3 instance plane).
		if opts.Agents != nil {
			opts.Agents.Register(mux, paProtect, paMutate)
		}
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

type projectRoleDTO struct {
	Project string `json:"project"`
	Role    string `json:"role"`
}

// intersectProjectRoles keeps only grants for projects the user currently belongs
// to, so a stale grant for a project the user has left never surfaces in the nav.
func intersectProjectRoles(grants []store.ProjectRole, members map[string]bool) []projectRoleDTO {
	out := []projectRoleDTO{}
	for _, g := range grants {
		if members[g.Project] {
			out = append(out, projectRoleDTO{Project: g.Project, Role: g.Role})
		}
	}
	return out
}

// ---- Handlers ----

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	prs := s.projectRolesFor(r.Context(), u)
	writeJSON(w, http.StatusOK, map[string]any{
		"email":                u.Email,
		"handle":               u.Handle,
		"groups":               u.Groups,
		"roles":                rbac.RolesFor(u.Groups),
		"project_roles":        prs,
		"project_capabilities": s.projectCapabilitiesFor(r.Context(), u, prs),
		"local_ssh":            s.svc.LocalSSHEnabled(),
	})
}

type projectCapsDTO struct {
	Project      string   `json:"project"`
	Capabilities []string `json:"capabilities"`
}

// projectCapabilitiesFor computes the caller's effective capabilities for every
// member project: explicit grants plus the implicit project-user baseline,
// resolved against the project's matrix (stored row or defaults). Best-effort
// like projectRolesFor — a failure logs and yields an empty list, never a 500.
func (s *server) projectCapabilitiesFor(ctx context.Context, u auth.User, grants []projectRoleDTO) []projectCapsDTO {
	if s.caps == nil {
		return []projectCapsDTO{}
	}
	ds, err := s.svc.ListProjects(ctx, u.Groups)
	if err != nil {
		slog.Warn("me: project listing for capabilities failed", "err", err)
		return []projectCapsDTO{}
	}
	byProject := map[string][]string{}
	for _, g := range grants {
		byProject[g.Project] = append(byProject[g.Project], g.Role)
	}
	out := []projectCapsDTO{}
	for _, d := range ds {
		roles := append(byProject[d.ProjectName], string(rbac.RoleProjectUser))
		matrix, stored, err := s.caps.GetCapabilityMatrix(ctx, d.ProjectName)
		if err != nil {
			slog.Warn("me: capability matrix lookup failed", "project", d.ProjectName, "err", err)
			continue
		}
		if !stored {
			matrix = rbac.DefaultCapabilityMatrix()
		}
		out = append(out, projectCapsDTO{Project: d.ProjectName, Capabilities: rbac.EffectiveCapabilities(matrix, roles)})
	}
	return out
}

// projectRolesFor returns the caller's project roles, intersected with current
// memberships. The common case (no grants) short-circuits before any Vault call.
// If the membership listing fails, it logs and returns the un-intersected grants
// rather than failing /api/me.
func (s *server) projectRolesFor(ctx context.Context, u auth.User) []projectRoleDTO {
	if s.roles == nil {
		return []projectRoleDTO{}
	}
	grants, err := s.roles.ForSubject(ctx, u.Email)
	if err != nil {
		slog.Warn("me: project roles lookup failed", "err", err)
		return []projectRoleDTO{}
	}
	if len(grants) == 0 {
		return []projectRoleDTO{}
	}
	ds, err := s.svc.ListProjects(ctx, u.Groups)
	if err != nil {
		slog.Warn("me: membership filter unavailable; returning unfiltered project roles", "err", err)
		members := map[string]bool{}
		for _, g := range grants {
			members[g.Project] = true
		}
		return intersectProjectRoles(grants, members)
	}
	members := make(map[string]bool, len(ds))
	for _, d := range ds {
		members[d.ProjectName] = true
	}
	return intersectProjectRoles(grants, members)
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
		Flavor        string   `json:"flavor"`
		SharedVolumes []string `json:"shared_volumes"`
	}
	// Flavor is optional for single-flavor projects and shared_volumes may be empty,
	// so an empty body is valid; reject only malformed JSON.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	ws, err := s.svc.Create(r.Context(), d, workspace.CreateInput{
		Flavor:        body.Flavor,
		Email:         u.Email,
		Handle:        u.Handle,
		GitName:       u.GitName,
		SharedVolumes: body.SharedVolumes,
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
