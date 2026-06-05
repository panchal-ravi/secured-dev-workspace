// Package workspace orchestrates workspace creation and listing by composing the
// HashiStack clients, descriptor metadata, job-template rendering, and port
// allocation — the direct-API equivalent of the dev-workspace Terraform tier.
package workspace

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/hashistack"
	"github.com/secured-dev-workspace/developer-portal/internal/jobrender"
	"github.com/secured-dev-workspace/developer-portal/internal/localssh"
	"github.com/secured-dev-workspace/developer-portal/internal/portgen"
)

const sessionMaxSeconds = 28800 // 8h, matching the workspace tier default

// Config is the subset of portal config the service needs.
type Config struct {
	BoundaryAddr string
	PortRange    portgen.Range
	// SSHConfigPath enables "Open in IDE" by writing per-workspace blocks into
	// this local SSH config. Empty disables the feature (production mode).
	SSHConfigPath string
}

// LocalSSHEnabled reports whether the portal can write the developer's SSH
// config (PoC local mode), gating the "Open in IDE" UI.
func (s *Service) LocalSSHEnabled() bool { return s.cfg.SSHConfigPath != "" }

// Service performs workspace operations against a single HashiStack.
type Service struct {
	cfg   Config
	vault *hashistack.Vault
	nomad *hashistack.Nomad
	bndry *hashistack.Boundary
}

func New(cfg Config, v *hashistack.Vault, n *hashistack.Nomad, b *hashistack.Boundary) *Service {
	return &Service{cfg: cfg, vault: v, nomad: n, bndry: b}
}

// CreateInput is the form payload plus the authenticated developer's identity.
// The workspace name, port, repo and git identity are all derived — the only
// developer-facing choice is the flavor (and that is optional when the project
// publishes exactly one).
type CreateInput struct {
	Flavor  string
	Email   string
	Handle  string
	GitName string
}

// Workspace is the UI view of one workspace, including ready-to-use connection
// configs and the flavor's feature list.
type Workspace struct {
	Name                    string               `json:"name"`           // ws-<handle>-<ws>
	WorkspaceName           string               `json:"workspace_name"` // <ws>
	Project                 string               `json:"project"`        // owning project, for the cross-project view
	Status                  string               `json:"status"`
	Port                    int                  `json:"port"`
	TargetID                string               `json:"target_id"`
	Alias                   string               `json:"alias"`
	Features                []descriptor.Feature `json:"features"`
	ProxyCommandConfig      string               `json:"proxycommand_config"`
	TransparentConfig       string               `json:"transparent_config"`
	BoundaryAuthenticateCmd string               `json:"boundary_authenticate_cmd"`
}

// ListProjects returns the descriptors the developer's groups may access.
func (s *Service) ListProjects(ctx context.Context, groups []string) ([]descriptor.Descriptor, error) {
	all, err := s.vault.ListDescriptors(ctx)
	if err != nil {
		return nil, err
	}
	return descriptor.Visible(all, groups), nil
}

// GetProject reads one descriptor and enforces group access.
func (s *Service) GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error) {
	d, err := s.vault.ReadDescriptor(ctx, name)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	if !d.AllowsGroups(groups) {
		return descriptor.Descriptor{}, fmt.Errorf("forbidden: not a member of %q", d.DevelopersGroupName)
	}
	return d, nil
}

// ListWorkspaces returns the developer's workspaces in the project.
func (s *Service) ListWorkspaces(ctx context.Context, d descriptor.Descriptor, handle string) ([]Workspace, error) {
	prefix := "ws-" + handle + "-"
	jobs, err := s.nomad.ListWorkspaceJobs(d.Namespace, prefix)
	if err != nil {
		return nil, err
	}
	targetIDs, err := s.bndry.TargetIDsByPrefix(ctx, d.ProjectScopeID, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(jobs))
	for _, j := range jobs {
		wsName := strings.TrimPrefix(j.Name, prefix)
		out = append(out, s.view(d, j.Name, wsName, j.Status, j.Port, targetIDs[j.Name], handle))
	}
	return out, nil
}

// ListAllWorkspaces returns the developer's workspaces across every project
// their groups grant — the data behind the global "My Workspaces" view.
func (s *Service) ListAllWorkspaces(ctx context.Context, groups []string, handle string) ([]Workspace, error) {
	projects, err := s.ListProjects(ctx, groups)
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0)
	for _, d := range projects {
		wss, err := s.ListWorkspaces(ctx, d, handle)
		if err != nil {
			return nil, err
		}
		out = append(out, wss...)
	}
	return out, nil
}

// Create provisions a new workspace end to end (Nomad volume + job, Boundary
// access graph), mirroring terraform/workspace.
func (s *Service) Create(ctx context.Context, d descriptor.Descriptor, in CreateInput) (Workspace, error) {
	// Flavor is optional when the project publishes exactly one.
	if in.Flavor == "" && len(d.Flavors) == 1 {
		in.Flavor = d.Flavors[0].Name
	}
	flavor, ok := d.Flavor(in.Flavor)
	if !ok {
		return Workspace{}, fmt.Errorf("unknown flavor %q for project %q", in.Flavor, d.ProjectName)
	}

	// The workspace identity is machine-generated — the developer names nothing.
	// A random suffix keeps the job/volume/alias names unique (no collision error).
	wsName, name, volume, err := s.allocateName(d.Namespace, in.Handle)
	if err != nil {
		return Workspace{}, err
	}

	used, err := s.nomad.UsedPorts()
	if err != nil {
		return Workspace{}, err
	}
	port, err := portgen.Allocate(s.cfg.PortRange, used)
	if err != nil {
		return Workspace{}, err
	}

	// The template already carries this project's static values (baked in at
	// publish time, see terraform/project/kv.tf); the portal fills only the
	// per-workspace placeholders.
	jobspec, err := s.vault.ReadJobTemplate(ctx, d.ProjectName, in.Flavor)
	if err != nil {
		return Workspace{}, err
	}
	rendered, err := jobrender.Render(jobspec, map[string]string{
		"job_name":        name,
		"ssh_port":        strconv.Itoa(port),
		"volume_name":     volume,
		"developer_email": in.Email,
		"git_user_name":   in.GitName,
	})
	if err != nil {
		return Workspace{}, err
	}

	if err := s.nomad.CreateHostVolume(d.Namespace, volume, flavor.NodePool); err != nil {
		return Workspace{}, err
	}
	jobID, err := s.nomad.RegisterJob(d.Namespace, rendered)
	if err != nil {
		return Workspace{}, err
	}

	// Default to the main node; a pooled flavor (e.g. GPU) lands on a different
	// node, so point Boundary at that node's IP resolved from the placed alloc.
	hostAddr := d.InstancePrivateIP
	if flavor.NodePool != "" {
		ip, err := s.nomad.ResolvePlacementIP(d.Namespace, jobID)
		if err != nil {
			return Workspace{}, err
		}
		hostAddr = ip
	}

	alias := fmt.Sprintf("%s.%s.%s.%s", wsName, in.Handle, d.ProjectName, d.AliasSuffix)
	res, err := s.bndry.Provision(ctx, hashistack.ProvisionInput{
		ScopeID:             d.ProjectScopeID,
		Name:                name,
		HostAddress:         hostAddr,
		DefaultPort:         uint32(port),
		CredentialLibraryID: d.CredentialLibraryID,
		SessionMaxSeconds:   sessionMaxSeconds,
		OIDCAuthMethodID:    d.BoundaryOIDCAuthMethodID,
		DeveloperEmail:      in.Email,
		DeveloperHandle:     in.Handle,
		AliasValue:          alias,
	})
	if err != nil {
		return Workspace{}, err
	}

	ws := s.view(d, name, wsName, "pending", port, res.TargetID, in.Handle)
	ws.Features = flavor.Features // exact flavor known on create
	return ws, nil
}

// Stop stops a developer's workspace job, leaving the home volume and Boundary
// access in place so it can be restarted.
func (s *Service) Stop(ctx context.Context, d descriptor.Descriptor, handle, jobName string) error {
	if err := requireOwned(handle, jobName); err != nil {
		return err
	}
	return s.nomad.StopJob(d.Namespace, jobName)
}

// Start restarts a previously stopped workspace job.
func (s *Service) Start(ctx context.Context, d descriptor.Descriptor, handle, jobName string) error {
	if err := requireOwned(handle, jobName); err != nil {
		return err
	}
	return s.nomad.StartJob(d.Namespace, jobName)
}

// Destroy tears a workspace down completely: purge the Nomad job, delete its
// home volume, and remove the Boundary graph. The volume and alias names derive
// from the job name exactly as allocateName built them.
func (s *Service) Destroy(ctx context.Context, d descriptor.Descriptor, handle, jobName string) error {
	if err := requireOwned(handle, jobName); err != nil {
		return err
	}
	wsName := strings.TrimPrefix(jobName, "ws-"+handle+"-")
	volume := "home-" + handle + "-" + wsName
	alias := fmt.Sprintf("%s.%s.%s.%s", wsName, handle, d.ProjectName, d.AliasSuffix)

	if err := s.nomad.PurgeJob(d.Namespace, jobName); err != nil {
		return err
	}
	if err := s.nomad.DeleteHostVolume(d.Namespace, volume); err != nil {
		return err
	}
	if err := s.bndry.Destroy(ctx, hashistack.DestroyInput{
		ScopeID:         d.ProjectScopeID,
		Name:            jobName,
		DeveloperHandle: handle,
		AliasValue:      alias,
	}); err != nil {
		return err
	}
	// Best-effort: drop the workspace's SSH config block so a destroyed
	// workspace leaves no stale Host entry behind (local mode only).
	if s.cfg.SSHConfigPath != "" {
		_ = localssh.Remove(s.cfg.SSHConfigPath, jobName)
	}
	return nil
}

// Logs returns the tail of the workspace job's stdout or stderr.
func (s *Service) Logs(ctx context.Context, d descriptor.Descriptor, handle, jobName, logType string) (string, error) {
	if err := requireOwned(handle, jobName); err != nil {
		return "", err
	}
	return s.nomad.JobLogs(d.Namespace, jobName, logType, 50*1024)
}

// WriteSSHConfig upserts the workspace's ProxyCommand block into the local SSH
// config and returns the Host label the developer's IDE connects to. The block
// is rebuilt server-side (not taken from the client) so only portal-generated
// config is ever written to disk.
func (s *Service) WriteSSHConfig(ctx context.Context, d descriptor.Descriptor, handle, jobName string) (string, error) {
	if err := requireOwned(handle, jobName); err != nil {
		return "", err
	}
	if s.cfg.SSHConfigPath == "" {
		return "", fmt.Errorf("invalid: writing SSH config is disabled on this portal")
	}
	ws, err := s.findWorkspace(ctx, d, handle, jobName)
	if err != nil {
		return "", err
	}
	if err := localssh.Upsert(s.cfg.SSHConfigPath, jobName, ws.ProxyCommandConfig); err != nil {
		return "", err
	}
	return ws.Name, nil
}

// findWorkspace resolves a single workspace (with its target id and port) by job
// name from the developer's listing.
func (s *Service) findWorkspace(ctx context.Context, d descriptor.Descriptor, handle, jobName string) (Workspace, error) {
	wss, err := s.ListWorkspaces(ctx, d, handle)
	if err != nil {
		return Workspace{}, err
	}
	for _, w := range wss {
		if w.Name == jobName {
			return w, nil
		}
	}
	return Workspace{}, fmt.Errorf("workspace %q not found", jobName)
}

// requireOwned guards that jobName belongs to handle (named ws-<handle>-...), so
// a developer can only act on their own workspaces.
func requireOwned(handle, jobName string) error {
	if !strings.HasPrefix(jobName, "ws-"+handle+"-") {
		return fmt.Errorf("forbidden: workspace %q does not belong to you", jobName)
	}
	return nil
}

// view assembles the UI workspace, including both connection configs.
func (s *Service) view(d descriptor.Descriptor, name, wsName, status string, port int, targetID, handle string) Workspace {
	user := d.WorkspaceUser
	if user == "" {
		user = "dev"
	}
	alias := fmt.Sprintf("%s.%s.%s.%s", wsName, handle, d.ProjectName, d.AliasSuffix)
	return Workspace{
		Name:                    name,
		WorkspaceName:           wsName,
		Project:                 d.ProjectName,
		Status:                  status,
		Port:                    port,
		TargetID:                targetID,
		Alias:                   alias,
		Features:                singleFlavorFeatures(d),
		ProxyCommandConfig:      ProxyCommandConfig(name, user, s.cfg.BoundaryAddr, targetID),
		TransparentConfig:       TransparentConfig(alias, user),
		BoundaryAuthenticateCmd: BoundaryAuthenticateCmd(s.cfg.BoundaryAddr, d.BoundaryOIDCAuthMethodID),
	}
}

// singleFlavorFeatures returns the feature list when the project has exactly one
// flavor (the PoC case). Listed workspaces don't record which flavor produced
// them, so with multiple flavors we cannot attribute features and return none.
func singleFlavorFeatures(d descriptor.Descriptor) []descriptor.Feature {
	if len(d.Flavors) == 1 {
		return d.Flavors[0].Features
	}
	return nil
}

const suffixLen = 5

// allocateName generates a unique workspace identity. The short name is random
// (the developer chooses nothing); the job/volume names derive from it. It checks
// Nomad for a collision and regenerates, which is effectively never needed but
// makes uniqueness guaranteed rather than probabilistic.
func (s *Service) allocateName(namespace, handle string) (wsName, name, volume string, err error) {
	for attempt := 0; attempt < 5; attempt++ {
		wsName = randSuffix(suffixLen)
		name = "ws-" + handle + "-" + wsName
		volume = "home-" + handle + "-" + wsName
		exists, e := s.nomad.JobExists(namespace, name)
		if e != nil {
			return "", "", "", e
		}
		if !exists {
			return wsName, name, volume, nil
		}
	}
	return "", "", "", fmt.Errorf("could not allocate a unique workspace name after 5 attempts")
}

// suffixAlphabet is lowercase alphanumerics — safe in a Nomad job ID, a Boundary
// name, and a Boundary alias label.
const suffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// randSuffix returns an n-character random identifier. The modulo skew across 36
// symbols is immaterial for a non-secret workspace id.
func randSuffix(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("workspace: crypto/rand failed: %v", err))
	}
	for i := range b {
		b[i] = suffixAlphabet[int(b[i])%len(suffixAlphabet)]
	}
	return string(b)
}
