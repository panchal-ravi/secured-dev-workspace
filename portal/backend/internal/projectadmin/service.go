// Package projectadmin is the project-facing onboarding plane: a project-admin
// authors and deploys an MCP server directly into their project — image,
// transport and credential config in one step. The deploy instantiates the
// credential spec into the project's Vault namespace and binds the minted WIF
// role into the Nomad job — credentials are Vault-brokered, never pasted into
// the job. There is no platform catalog: the project-admin owns the definition.
package projectadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpjob"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

type ProjectLookup interface {
	GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error)
}

type Executor interface {
	Instantiate(ctx context.Context, serverName string, spec blueprint.CredentialSpec, namespace string, params map[string]string, grants []blueprint.PathGrant) (blueprint.InstanceRecord, error)
	UpdateGrants(ctx context.Context, rec blueprint.InstanceRecord, spec blueprint.CredentialSpec, serverName string, grants []blueprint.PathGrant) (blueprint.InstanceRecord, error)
	Deprovision(ctx context.Context, rec blueprint.InstanceRecord) error
}

type NomadClient interface {
	RegisterJob(namespace, jobHCL, flavor string) (string, error)
	ResolvePlacement(namespace, jobID string) (ip string, port int, err error)
	PurgeJob(namespace, jobID string) error
	JobExists(namespace, jobID string) (bool, error)
}

// Wirer re-runs the workspace-consumption wiring (gateway virtual server +
// scoped token + KV) for a deployed server. Implemented by the projectengines
// service; nil disables auto-rewire.
type Wirer interface {
	WireMCPServer(ctx context.Context, project, serverName string) error
}

type Config struct {
	NodePool string
}

type Service struct {
	store    store.Store
	projects ProjectLookup
	executor Executor
	nomad    NomadClient
	gateway  mcpgw.Client
	wirer    Wirer
	cfg      Config
}

func New(st store.Store, projects ProjectLookup, ex Executor, nomad NomadClient, gateway mcpgw.Client, wirer Wirer, cfg Config) *Service {
	return &Service{store: st, projects: projects, executor: ex, nomad: nomad, gateway: gateway, wirer: wirer, cfg: cfg}
}

type DeployedView struct {
	store.ProjectMCPServer
	Running bool `json:"running"`
}

// ListResult keeps the historical "deployed" JSON key (Templates.tsx and the
// project McpServers page consume it).
type ListResult struct {
	Deployed []DeployedView `json:"deployed"`
}

// List returns the project's deployed MCP servers with live job status.
func (s *Service) List(ctx context.Context, groups []string, project string) (ListResult, error) {
	d, err := s.projects.GetProject(ctx, project, groups) // membership + the namespace for live status
	if err != nil {
		return ListResult{}, err
	}
	rows, err := s.store.ListProjectMCPServers(ctx, project)
	if err != nil {
		return ListResult{}, err
	}
	out := ListResult{Deployed: []DeployedView{}}
	for _, r := range rows {
		running := false
		if r.JobID != "" {
			running, _ = s.nomad.JobExists(d.Namespace, r.JobID)
		}
		out.Deployed = append(out.Deployed, DeployedView{ProjectMCPServer: r, Running: running})
	}
	return out, nil
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// DeployInput is the full server definition + credential config a project-admin
// authors in the deploy wizard. Secret-typed param values are used once and never
// persisted.
type DeployInput struct {
	Name        string                   `json:"name"`
	Image       string                   `json:"image"`
	Command     []string                 `json:"command,omitempty"`
	Env         map[string]string        `json:"env,omitempty"`
	Transport   string                   `json:"transport"` // sse | streamable-http
	Port        int                      `json:"port"`      // container port; the host side is dynamic
	Path        string                   `json:"path,omitempty"`
	Credential  blueprint.CredentialSpec `json:"credential"`
	Params      map[string]string        `json:"params,omitempty"`
	ExtraGrants []blueprint.PathGrant    `json:"extra_grants,omitempty"`
}

func (in DeployInput) validate() error {
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("name must be 3-40 chars, lowercase alphanumeric or dashes: %w", apperr.ErrBadRequest)
	}
	if err := validateServerDef(in.Image, in.Transport, in.Port); err != nil {
		return err
	}
	return in.Credential.Validate()
}

// validateServerDef checks the container-definition fields shared by deploy and
// update.
func validateServerDef(image, transport string, port int) error {
	if image == "" {
		return fmt.Errorf("image is required: %w", apperr.ErrBadRequest)
	}
	if _, ok := defaultPaths[transport]; !ok {
		return fmt.Errorf("transport must be sse or streamable-http (stdio needs the auth wrapper): %w", apperr.ErrBadRequest)
	}
	// The container port is exposed via a Nomad DYNAMIC host port, so no static
	// band constraint applies — it just has to be a real port.
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be 1-65535: %w", apperr.ErrBadRequest)
	}
	return nil
}

// DeployServer instantiates the credential spec into the project's Vault
// namespace, renders a Nomad job bound to the minted WIF role with the credential
// env templates, registers it, and records the deployed server. Any failure AFTER
// a successful Instantiate triggers a best-effort Deprovision (the executor is
// idempotent), so a project never accrues orphan Vault state.
func (s *Service) DeployServer(ctx context.Context, actor string, groups []string, project string, in DeployInput) (store.ProjectMCPServer, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	ns := d.Namespace
	if ns == "" {
		return store.ProjectMCPServer{}, fmt.Errorf("project %q has no namespace: %w", project, apperr.ErrBadRequest)
	}
	if err := in.validate(); err != nil {
		return store.ProjectMCPServer{}, err
	}

	if _, err := s.store.GetProjectMCPServer(ctx, project, in.Name); err == nil {
		return store.ProjectMCPServer{}, fmt.Errorf("server %q already deployed in %q: %w", in.Name, project, apperr.ErrConflict)
	}
	if err := s.rejectMountCollision(ctx, project, in); err != nil {
		return store.ProjectMCPServer{}, err
	}

	// source=none needs nothing in Vault: no instance, no WIF vault block.
	var rec blueprint.InstanceRecord
	provisioned := in.Credential.Source != blueprint.SourceNone
	if provisioned {
		rec, err = s.executor.Instantiate(ctx, in.Name, in.Credential, ns, in.Params, in.ExtraGrants)
		if err != nil {
			s.audit(ctx, actor, "project-mcp.deploy", project+"/"+in.Name, "error", map[string]any{"stage": "instantiate"})
			return store.ProjectMCPServer{}, err
		}
	} else if len(in.ExtraGrants) > 0 {
		return store.ProjectMCPServer{}, fmt.Errorf("path grants need a credential source (no WIF token exists to carry them): %w", apperr.ErrBadRequest)
	}

	fail := func(stage string, err error) (store.ProjectMCPServer, error) {
		if provisioned {
			_ = s.executor.Deprovision(ctx, rec)
		}
		s.audit(ctx, actor, "project-mcp.deploy", project+"/"+in.Name, "error", map[string]any{"stage": stage})
		return store.ProjectMCPServer{}, err
	}

	var cred mcpjob.Credential
	if provisioned {
		credEnv, err := mcpjob.CredentialEnv(in.Credential.EnvTemplates, rec, in.Params)
		if err != nil {
			return fail("credential-env", err)
		}
		cred = mcpjob.WIFCredential{VaultNamespace: ns, WIFRole: rec.WIFRoleName, EnvTemplates: credEnv}
	}
	hcl := mcpjob.Render(mcpjob.RenderSpec{
		JobName:     jobName(project, in.Name),
		Namespace:   ns,
		NodePool:    s.cfg.NodePool,
		Image:       in.Image,
		Command:     in.Command,
		Port:        in.Port,
		Env:         in.Env,
		ServiceName: serviceName(project, in.Name),
		Tags:        projectDiscoveryTags(project, in),
		Credential:  cred,
		DynamicPort: true,
	})
	jobID, err := s.nomad.RegisterJob(ns, hcl, "")
	if err != nil {
		return fail("register-job", err)
	}
	ip, hostPort, err := s.nomad.ResolvePlacement(ns, jobID)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("resolve-placement", err)
	}
	if hostPort == 0 {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("resolve-placement", fmt.Errorf("nomad: no http host port assigned for %q", jobID))
	}

	instBlob, err := json.Marshal(rec)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("marshal-instance", err)
	}
	credBlob, err := json.Marshal(in.Credential)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("marshal-credential", err)
	}
	row := store.ProjectMCPServer{
		Project: project, Name: in.Name, Status: store.StatusDeployed,
		Image: in.Image, Command: in.Command, Env: in.Env, Port: in.Port, Path: in.Path,
		Credential: credBlob, Params: in.Credential.NonSecretParams(in.Params),
		Instance: instBlob, JobID: jobID,
		GatewayURL: peerURL(ip, hostPort, in.Transport, in.Path), Transport: in.Transport, CreatedBy: actor,
	}
	saved, err := s.store.UpsertProjectMCPServer(ctx, row)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("persist", err)
	}
	s.audit(ctx, actor, "project-mcp.deploy", project+"/"+in.Name, "ok", nil)
	if err := s.rewireIfReferenced(ctx, actor, project, in.Name); err != nil {
		return saved, err
	}
	return saved, nil
}

// rewireIfReferenced re-runs the workspace wiring when a template add-on already
// references the server: a redeploy would otherwise leave workspaces pointed at
// the previous deploy's (now dead) virtual server until someone remembers to
// re-run Apply add-ons. A wire failure does not undo the deploy — the error
// names the one manual recovery step.
func (s *Service) rewireIfReferenced(ctx context.Context, actor, project, name string) error {
	if s.wirer == nil {
		return nil
	}
	pts, err := s.store.ListProjectTemplates(ctx, project)
	if err != nil {
		// ErrConflict ("deployed but not wired" is a state the admin must
		// resolve) so the api layer forwards the remediation message instead of
		// flattening it to a generic upstream error.
		return fmt.Errorf("server deployed, but listing templates to re-wire failed: %v — re-run Apply add-ons on the template: %w", err, apperr.ErrConflict)
	}
	referenced := false
	for _, pt := range pts {
		for _, n := range pt.Addons.MCPServers {
			if n == name {
				referenced = true
			}
		}
	}
	if !referenced {
		return nil
	}
	if err := s.wirer.WireMCPServer(ctx, project, name); err != nil {
		s.audit(ctx, actor, "project-mcp.rewire", project+"/"+name, "error", nil)
		return fmt.Errorf("server deployed, but re-wiring workspace templates failed: %v — re-run Apply add-ons on the template: %w", err, apperr.ErrConflict)
	}
	s.audit(ctx, actor, "project-mcp.rewire", project+"/"+name, "ok", nil)
	return nil
}

// rejectMountCollision refuses a dynamic deploy whose engine mount is already
// claimed by another server in the project: deprovisioning one would unmount the
// other's engine (the InstanceRecord records the mount for teardown).
func (s *Service) rejectMountCollision(ctx context.Context, project string, in DeployInput) error {
	if in.Credential.Source != blueprint.SourceDynamic {
		return nil
	}
	rows, err := s.store.ListProjectMCPServers(ctx, project)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if len(r.Credential) == 0 {
			continue
		}
		var other blueprint.CredentialSpec
		if err := json.Unmarshal(r.Credential, &other); err != nil {
			continue
		}
		if other.Source == blueprint.SourceDynamic && other.Dynamic != nil &&
			other.Dynamic.Mount == in.Credential.Dynamic.Mount {
			return fmt.Errorf("engine mount %q is already used by server %q (deleting one would unmount the other): %w",
				in.Credential.Dynamic.Mount, r.Name, apperr.ErrConflict)
		}
	}
	return nil
}

// UpdateInput is the editable slice of a deployed server: the full container
// definition (image, command, transport, port, path, env — sent whole, replacing
// what's stored) plus the extra Vault path grants. Only the credential source
// still requires delete + redeploy: changing it changes what exists in Vault
// (policy, WIF role, secret layout), not just the running container.
type UpdateInput struct {
	Image       string                `json:"image"`
	Command     []string              `json:"command,omitempty"`
	Transport   string                `json:"transport"`
	Port        int                   `json:"port"`
	Path        string                `json:"path,omitempty"`
	Env         map[string]string     `json:"env"`
	ExtraGrants []blueprint.PathGrant `json:"extra_grants"`
}

// UpdateServer edits a deployed server in place. The Vault policy is rewritten
// from the persisted credential spec + the replacement grants (Vault evaluates
// policies at request time — immediate, zero churn). If the container definition
// changed, the Nomad job is re-rendered and resubmitted under the same name,
// then the gateway peer is updated IN PLACE at the new dynamic address — the
// wired virtual server and its scoped token survive, so existing workspaces
// keep working.
func (s *Service) UpdateServer(ctx context.Context, actor string, groups []string, project, name string, in UpdateInput) (store.ProjectMCPServer, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	if err := validateServerDef(in.Image, in.Transport, in.Port); err != nil {
		return store.ProjectMCPServer{}, err
	}
	row, err := s.store.GetProjectMCPServer(ctx, project, name)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	var rec blueprint.InstanceRecord
	if len(row.Instance) > 0 {
		if err := json.Unmarshal(row.Instance, &rec); err != nil {
			return store.ProjectMCPServer{}, fmt.Errorf("parse persisted instance: %w", apperr.ErrBadRequest)
		}
	}
	var spec blueprint.CredentialSpec
	if len(row.Credential) > 0 {
		if err := json.Unmarshal(row.Credential, &spec); err != nil {
			return store.ProjectMCPServer{}, fmt.Errorf("parse persisted credential: %w", apperr.ErrBadRequest)
		}
	}
	provisioned := spec.Source != "" && spec.Source != blueprint.SourceNone
	if !provisioned && len(in.ExtraGrants) > 0 {
		return store.ProjectMCPServer{}, fmt.Errorf("path grants need a credential source (no WIF token exists to carry them): %w", apperr.ErrBadRequest)
	}
	if provisioned {
		updated, err := s.executor.UpdateGrants(ctx, rec, spec, name, in.ExtraGrants)
		if err != nil {
			s.audit(ctx, actor, "project-mcp.update", project+"/"+name, "error", nil)
			return store.ProjectMCPServer{}, err
		}
		rec = updated
		blob, err := json.Marshal(rec)
		if err != nil {
			return store.ProjectMCPServer{}, err
		}
		row.Instance = blob
	}

	defChanged := row.Image != in.Image || !slices.Equal(row.Command, in.Command) ||
		row.Transport != in.Transport || row.Port != in.Port || row.Path != in.Path ||
		!maps.Equal(row.Env, in.Env)
	if defChanged {
		var cred mcpjob.Credential
		if provisioned {
			credEnv, err := mcpjob.CredentialEnv(spec.EnvTemplates, rec, row.Params)
			if err != nil {
				return store.ProjectMCPServer{}, err
			}
			cred = mcpjob.WIFCredential{VaultNamespace: d.Namespace, WIFRole: rec.WIFRoleName, EnvTemplates: credEnv}
		}
		hcl := mcpjob.Render(mcpjob.RenderSpec{
			JobName:     jobName(project, name),
			Namespace:   d.Namespace,
			NodePool:    s.cfg.NodePool,
			Image:       in.Image,
			Command:     in.Command,
			Port:        in.Port,
			Env:         in.Env,
			ServiceName: serviceName(project, name),
			Tags:        projectDiscoveryTags(project, DeployInput{Name: name, Transport: in.Transport, Port: in.Port, Path: in.Path}),
			Credential:  cred,
			DynamicPort: true,
		})
		jobID, err := s.nomad.RegisterJob(d.Namespace, hcl, "")
		if err != nil {
			s.audit(ctx, actor, "project-mcp.update", project+"/"+name, "error", map[string]any{"stage": "register-job"})
			return store.ProjectMCPServer{}, err
		}
		ip, hostPort, err := s.nomad.ResolvePlacement(d.Namespace, jobID)
		if err == nil && hostPort == 0 {
			err = fmt.Errorf("nomad: no http host port assigned for %q", jobID)
		}
		if err != nil {
			// The job WAS updated — don't purge a previously-working server. The
			// admin fixes the definition and saves again (or deletes + redeploys).
			s.audit(ctx, actor, "project-mcp.update", project+"/"+name, "error", map[string]any{"stage": "resolve-placement"})
			return store.ProjectMCPServer{}, fmt.Errorf("job updated, but the new allocation is not reachable: %v — fix the definition and save again, or delete + redeploy: %w", err, apperr.ErrConflict)
		}
		row.Image, row.Command, row.Transport, row.Port, row.Path = in.Image, in.Command, in.Transport, in.Port, in.Path
		row.JobID = jobID
		row.Env = in.Env
		row.GatewayURL = peerURL(ip, hostPort, in.Transport, in.Path)
	}

	// The replacement allocation is at a new address, so the gateway peer's
	// upstream URL is stale. Prefer updating it IN PLACE: the peer keeps its
	// identity, so its tool records, the wired virtual server, and the scoped
	// token baked into existing workspaces all stay valid — no re-wire, no
	// workspace recreation. Only if that fails fall back to the destructive
	// delete + re-wire (fresh VS + client token ⇒ workspaces must be recreated).
	peerHealed := false
	if defChanged {
		peerID := row.PeerID
		var peerErr error
		if peerID == "" {
			// The wirer registers the peer without recording its id on the row —
			// find (or, if none exists yet, create at the new URL) by the
			// canonical name both planes use.
			peerID, peerErr = s.gateway.RegisterPeer(ctx, serviceName(project, name), row.GatewayURL, row.Transport)
		}
		if peerErr == nil {
			peerErr = s.gateway.UpdatePeer(ctx, peerID, serviceName(project, name), row.GatewayURL, row.Transport)
		}
		if peerErr == nil {
			// ContextForge deactivates a peer after 3 failed health checks — the
			// restart window can be enough. Re-activate so the tools re-federate
			// under their existing ids (no-op if the peer never went inactive).
			peerErr = s.gateway.ActivatePeer(ctx, peerID)
		}
		if peerErr == nil {
			row.PeerID = peerID
			peerHealed = true
		} else {
			if peerID != "" {
				_ = s.gateway.DeletePeer(ctx, peerID)
			}
			row.PeerID = ""
		}
	}

	saved, err := s.store.UpsertProjectMCPServer(ctx, row)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	s.audit(ctx, actor, "project-mcp.update", project+"/"+name, "ok", nil)
	if defChanged && !peerHealed {
		if err := s.rewireIfReferenced(ctx, actor, project, name); err != nil {
			return saved, err
		}
	}
	return saved, nil
}

// TestServer runs the consumption-mirror verification against a deployed project
// server: register it as a gateway peer, discover tools, compose a temporary
// virtual server + decoy + scoped token, confirm the token reaches only its own
// server (200) and is denied admin + the decoy (403), tear down the temp artifacts
// (the peer is kept), and record the result on the row.
func (s *Service) TestServer(ctx context.Context, actor string, groups []string, project, name string) (store.ProjectMCPServer, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	row, err := s.store.GetProjectMCPServer(ctx, project, name)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}

	// Reconcile the peer with the live allocation before probing: a Nomad
	// reschedule (or a heal that never landed) leaves the gateway dialing a dead
	// host port, and ContextForge deactivates such a peer after 3 failed health
	// checks. Test re-resolves the placement, rewrites the peer URL in place
	// (identity, tools, VS, tokens all survive) and re-activates it, so a stale
	// peer is repaired by the same button that reports it broken.
	ip, hostPort, err := s.nomad.ResolvePlacement(d.Namespace, row.JobID)
	if err == nil && hostPort == 0 {
		err = fmt.Errorf("nomad: no http host port assigned for %q", row.JobID)
	}
	if err != nil {
		s.audit(ctx, actor, "project-mcp.test", project+"/"+name, "error", nil)
		return store.ProjectMCPServer{}, err
	}
	row.GatewayURL = peerURL(ip, hostPort, row.Transport, row.Path)

	peerID, err := s.gateway.RegisterPeer(ctx, serviceName(project, name), row.GatewayURL, row.Transport)
	if err == nil {
		err = s.gateway.UpdatePeer(ctx, peerID, serviceName(project, name), row.GatewayURL, row.Transport)
	}
	if err == nil {
		err = s.gateway.ActivatePeer(ctx, peerID)
	}
	if err != nil {
		s.audit(ctx, actor, "project-mcp.test", project+"/"+name, "error", nil)
		return store.ProjectMCPServer{}, err
	}
	row.PeerID = peerID

	toolIDs, err := s.gateway.DiscoverTools(ctx, peerID)
	if err != nil {
		s.audit(ctx, actor, "project-mcp.test", project+"/"+name, "error", nil)
		return store.ProjectMCPServer{}, err
	}

	// The virtual servers + scoped token are throwaway probe scaffolding. Tear each
	// down with a best-effort defer registered at creation, so an error on any later
	// step still cleans up what was already created (the peer is kept, by design).
	base := serviceName(project, name)
	vsID, err := s.gateway.CreateVirtualServer(ctx, base+"-test", "consumption-mirror test for "+name, toolIDs)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	defer func() { _ = s.gateway.DeleteVirtualServer(ctx, vsID) }()
	decoyID, err := s.gateway.CreateVirtualServer(ctx, base+"-decoy", "isolation decoy for "+name, toolIDs)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	defer func() { _ = s.gateway.DeleteVirtualServer(ctx, decoyID) }()
	token, err := s.gateway.CreateScopedToken(ctx, base+"-test-"+randHex(4), 1, vsID)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	defer func() { _ = s.gateway.RevokeTokensByPrefix(ctx, base+"-test-") }()
	probe, err := s.gateway.ProbeScopedToken(ctx, token, vsID, decoyID)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}

	result := &store.MCPTestResult{
		Passed:             probe.Passed() && len(toolIDs) > 0,
		ToolsDiscovered:    len(toolIDs),
		OwnServerOK:        probe.OwnServerOK,
		AdminDenied:        probe.AdminDenied,
		OtherServerDenied:  probe.OtherServerDenied,
		OtherServerChecked: probe.OtherServerChecked,
		At:                 time.Now(),
	}
	if !result.Passed {
		result.Message = "consumption-mirror checks did not all pass"
	}
	row.TestResult = result

	saved, err := s.store.UpsertProjectMCPServer(ctx, row)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	outcome := "failed"
	if result.Passed {
		outcome = "passed"
	}
	s.audit(ctx, actor, "project-mcp.test", project+"/"+name, outcome, nil)
	return saved, nil
}

// DeleteServer tears a deployed server down lease-safely: stop the Nomad job,
// deregister the gateway peer, then Deprovision the persisted blueprint instance
// (the executor revokes dynamic leases BEFORE unmounting the engine), and drop the
// row. External steps are best-effort so a partial state still converges to clean.
func (s *Service) DeleteServer(ctx context.Context, actor string, groups []string, project, name string) error {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return err
	}
	row, err := s.store.GetProjectMCPServer(ctx, project, name)
	if err != nil {
		return err
	}
	if row.JobID != "" {
		_ = s.nomad.PurgeJob(d.Namespace, row.JobID)
	}
	if row.PeerID != "" {
		_ = s.gateway.DeletePeer(ctx, row.PeerID)
	}
	// The wire plane's artifacts must not outlive the server: deleting the peer
	// strips the virtual server's tool associations, leaving a VS that still
	// authenticates but serves zero tools (observed live — a workspace shows
	// "connected · no tools"). Remove the VS and revoke its client tokens so any
	// stale consumer fails loudly instead.
	base := serviceName(project, name)
	_ = s.gateway.DeleteVirtualServerByName(ctx, base)
	_ = s.gateway.RevokeTokensByPrefix(ctx, base+"-client")
	if len(row.Instance) > 0 {
		var rec blueprint.InstanceRecord
		if err := json.Unmarshal(row.Instance, &rec); err != nil {
			return fmt.Errorf("parse persisted instance: %w", apperr.ErrBadRequest)
		}
		if err := s.executor.Deprovision(ctx, rec); err != nil {
			s.audit(ctx, actor, "project-mcp.delete", project+"/"+name, "error", map[string]any{"stage": "deprovision"})
			return err
		}
	}
	if err := s.store.DeleteProjectMCPServer(ctx, project, name); err != nil {
		return err
	}
	s.audit(ctx, actor, "project-mcp.delete", project+"/"+name, "ok", nil)
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) audit(ctx context.Context, actor, action, target, outcome string, detail map[string]any) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Outcome: outcome, Detail: detail})
}
