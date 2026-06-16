// Package projectadmin is the project-facing onboarding plane: a project-admin
// deploys a published, blueprint-backed MCP server type into their project. The
// deploy instantiates the credential blueprint into the project's Vault namespace
// and binds the minted WIF role into the Nomad job — credentials are
// blueprint-provisioned, never pasted. It mirrors internal/admin but targets the
// project namespace (descriptor.Namespace) and uses a WIF credential.
package projectadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	Instantiate(ctx context.Context, m blueprint.BlueprintManifest, namespace string, params map[string]string) (blueprint.InstanceRecord, error)
	Deprovision(ctx context.Context, rec blueprint.InstanceRecord) error
}

type NomadClient interface {
	RegisterJob(namespace, jobHCL, flavor string) (string, error)
	ResolvePlacementIP(namespace, jobID string) (string, error)
	PurgeJob(namespace, jobID string) error
	JobExists(namespace, jobID string) (bool, error)
}

type VaultClient interface {
	ReadKVField(ctx context.Context, relPath, field string) (string, error)
}

type Config struct {
	BlueprintsKVPath string
	NodePool         string
}

func (c Config) withDefaults() Config {
	if c.BlueprintsKVPath == "" {
		c.BlueprintsKVPath = "infra/blueprints"
	}
	return c
}

type Service struct {
	store    store.Store
	projects ProjectLookup
	executor Executor
	nomad    NomadClient
	gateway  mcpgw.Client
	vault    VaultClient
	cfg      Config
}

func New(st store.Store, projects ProjectLookup, ex Executor, nomad NomadClient, gateway mcpgw.Client, vault VaultClient, cfg Config) *Service {
	return &Service{store: st, projects: projects, executor: ex, nomad: nomad, gateway: gateway, vault: vault, cfg: cfg.withDefaults()}
}

type DeployableType struct {
	Name      string                `json:"name"`
	Image     string                `json:"image"`
	Transport string                `json:"transport"`
	Params    []blueprint.ParamSpec `json:"params"`
}

type DeployedView struct {
	store.ProjectMCPServer
	Running bool `json:"running"`
}

type Catalog struct {
	Deployable []DeployableType `json:"deployable"`
	Deployed   []DeployedView   `json:"deployed"`
}

func (s *Service) ListDeployable(ctx context.Context, project string) (Catalog, error) {
	types, err := s.store.ListMCPServers(ctx)
	if err != nil {
		return Catalog{}, err
	}
	out := Catalog{Deployable: []DeployableType{}, Deployed: []DeployedView{}}
	for _, t := range types {
		if t.Status != store.StatusPublished || t.BlueprintRef == nil {
			continue
		}
		m, err := s.loadManifest(ctx, *t.BlueprintRef)
		if err != nil {
			return Catalog{}, err
		}
		out.Deployable = append(out.Deployable, DeployableType{Name: t.Name, Image: t.Image, Transport: t.Transport, Params: m.Params})
	}
	rows, err := s.store.ListProjectMCPServers(ctx, project)
	if err != nil {
		return Catalog{}, err
	}
	for _, r := range rows {
		running := false
		if r.JobID != "" {
			running, _ = s.nomad.JobExists(s.projectNamespaceBestEffort(ctx, project), r.JobID)
		}
		out.Deployed = append(out.Deployed, DeployedView{ProjectMCPServer: r, Running: running})
	}
	return out, nil
}

func (s *Service) projectNamespaceBestEffort(ctx context.Context, project string) string {
	d, err := s.projects.GetProject(ctx, project, nil)
	if err != nil || d.Namespace == "" {
		return project
	}
	return d.Namespace
}

func (s *Service) loadManifest(ctx context.Context, ref store.BlueprintRef) (blueprint.BlueprintManifest, error) {
	bp, err := s.store.GetBlueprint(ctx, ref.ID, ref.Version)
	if err != nil {
		return blueprint.BlueprintManifest{}, fmt.Errorf("blueprint %s@%d: %w", ref.ID, ref.Version, apperr.ErrNotFound)
	}
	if bp.ContentHash != ref.ContentHash {
		return blueprint.BlueprintManifest{}, fmt.Errorf("blueprint ref hash drift: %w", apperr.ErrBadRequest)
	}
	raw, err := s.vault.ReadKVField(ctx, fmt.Sprintf("%s/%s/%d", s.cfg.BlueprintsKVPath, ref.ID, ref.Version), "manifest")
	if err != nil {
		return blueprint.BlueprintManifest{}, err
	}
	var m blueprint.BlueprintManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return blueprint.BlueprintManifest{}, fmt.Errorf("parse manifest: %w", apperr.ErrBadRequest)
	}
	if m.ContentHash() != ref.ContentHash {
		return blueprint.BlueprintManifest{}, fmt.Errorf("stored manifest hash does not match ref: %w", apperr.ErrBadRequest)
	}
	return m, nil
}

// DeployInput is a request to deploy a published server type into a project.
type DeployInput struct {
	ServerType string            `json:"server_type"`
	Params     map[string]string `json:"params,omitempty"`
}

// DeployServer instantiates the server type's credential blueprint into the
// project's Vault namespace, renders a Nomad job bound to the minted WIF role with
// the blueprint credential template, registers it, and records the deployed server.
// Any failure AFTER a successful Instantiate triggers a best-effort Deprovision
// (the executor is idempotent), so a project never accrues orphan Vault state.
func (s *Service) DeployServer(ctx context.Context, actor string, groups []string, project string, in DeployInput) (store.ProjectMCPServer, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	ns := d.Namespace
	if ns == "" {
		return store.ProjectMCPServer{}, fmt.Errorf("project %q has no namespace: %w", project, apperr.ErrBadRequest)
	}

	t, err := s.store.GetMCPServer(ctx, in.ServerType)
	if err != nil {
		return store.ProjectMCPServer{}, fmt.Errorf("server type %q: %w", in.ServerType, apperr.ErrNotFound)
	}
	if t.Status != store.StatusPublished {
		return store.ProjectMCPServer{}, fmt.Errorf("server type %q is not published: %w", in.ServerType, apperr.ErrNotFound)
	}
	if t.BlueprintRef == nil {
		return store.ProjectMCPServer{}, fmt.Errorf("server type %q is not blueprint-backed: %w", in.ServerType, apperr.ErrBadRequest)
	}
	m, err := s.loadManifest(ctx, *t.BlueprintRef)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}

	if _, err := s.store.GetProjectMCPServer(ctx, project, t.Name); err == nil {
		return store.ProjectMCPServer{}, fmt.Errorf("server %q already deployed in %q: %w", t.Name, project, apperr.ErrConflict)
	}

	rec, err := s.executor.Instantiate(ctx, m, ns, in.Params)
	if err != nil {
		s.audit(ctx, actor, "project-mcp.deploy", project+"/"+t.Name, "error", map[string]any{"stage": "instantiate"})
		return store.ProjectMCPServer{}, err
	}

	fail := func(stage string, err error) (store.ProjectMCPServer, error) {
		_ = s.executor.Deprovision(ctx, rec)
		s.audit(ctx, actor, "project-mcp.deploy", project+"/"+t.Name, "error", map[string]any{"stage": stage})
		return store.ProjectMCPServer{}, err
	}

	credEnv, err := mcpjob.CredentialEnv(m.JobCredential, rec, in.Params)
	if err != nil {
		return fail("credential-env", err)
	}
	hcl := mcpjob.Render(mcpjob.RenderSpec{
		JobName:     jobName(project, t.Name),
		Namespace:   ns,
		NodePool:    s.cfg.NodePool,
		Image:       t.Image,
		Command:     t.Command,
		Port:        t.Port,
		Env:         t.Env,
		ServiceName: serviceName(project, t.Name),
		Tags:        projectDiscoveryTags(project, t),
		Credential:  mcpjob.WIFCredential{VaultNamespace: ns, WIFRole: rec.WIFRoleName, EnvTemplates: credEnv},
	})
	jobID, err := s.nomad.RegisterJob(ns, hcl, "")
	if err != nil {
		return fail("register-job", err)
	}
	ip, err := s.nomad.ResolvePlacementIP(ns, jobID)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("resolve-placement", err)
	}

	instBlob, err := json.Marshal(rec)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("marshal-instance", err)
	}
	row := store.ProjectMCPServer{
		Project: project, Name: t.Name, Status: store.StatusDeployed,
		BlueprintRef: *t.BlueprintRef, Instance: instBlob, JobID: jobID,
		GatewayURL: peerURL(ip, t), CreatedBy: actor,
	}
	saved, err := s.store.UpsertProjectMCPServer(ctx, row)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		return fail("persist", err)
	}
	s.audit(ctx, actor, "project-mcp.deploy", project+"/"+t.Name, "ok", nil)
	return saved, nil
}

// TestServer runs the consumption-mirror verification against a deployed project
// server: register it as a gateway peer, discover tools, compose a temporary
// virtual server + decoy + scoped token, confirm the token reaches only its own
// server (200) and is denied admin + the decoy (403), tear down the temp artifacts
// (the peer is kept), and record the result on the row.
func (s *Service) TestServer(ctx context.Context, actor string, groups []string, project, name string) (store.ProjectMCPServer, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return store.ProjectMCPServer{}, err
	}
	row, err := s.store.GetProjectMCPServer(ctx, project, name)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}

	peerID, err := s.gateway.RegisterPeer(ctx, serviceName(project, name), row.GatewayURL)
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

	base := serviceName(project, name)
	vsID, err := s.gateway.CreateVirtualServer(ctx, base+"-test", "consumption-mirror test for "+name, toolIDs)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	decoyID, err := s.gateway.CreateVirtualServer(ctx, base+"-decoy", "isolation decoy for "+name, toolIDs)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	token, err := s.gateway.CreateScopedToken(ctx, base+"-test-"+randHex(4), 1, vsID)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	probe, err := s.gateway.ProbeScopedToken(ctx, token, vsID, decoyID)
	if err != nil {
		return store.ProjectMCPServer{}, err
	}
	_ = s.gateway.RevokeTokensByPrefix(ctx, base+"-test-")
	_ = s.gateway.DeleteVirtualServer(ctx, vsID)
	_ = s.gateway.DeleteVirtualServer(ctx, decoyID)

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
