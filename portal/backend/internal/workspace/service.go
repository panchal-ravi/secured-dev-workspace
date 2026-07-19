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
	"sync"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/hashistack"
	"github.com/secured-dev-workspace/developer-portal/internal/jobrender"
	"github.com/secured-dev-workspace/developer-portal/internal/localssh"
	"github.com/secured-dev-workspace/developer-portal/internal/portgen"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

const sessionMaxSeconds = 28800 // 8h, matching the workspace tier default

// Config is the subset of portal config the service needs.
type Config struct {
	// BoundaryPublicAddr is the externally reachable Boundary address (the NLB)
	// embedded in the developer-facing authenticate/connect commands — distinct
	// from the loopback the portal's own on-node API client uses.
	BoundaryPublicAddr string
	PortRange          portgen.Range
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
	store store.Store
	nomad *hashistack.Nomad
	bndry *hashistack.Boundary

	// SSH ports are node-global and Create publishes its port to Nomad only
	// after a ~30s CSI volume wait, so UsedPorts() alone leaves a wide window in
	// which two concurrent creates pick the same port. mu + reserved close that
	// window: a port is held in-flight from allocation until Create returns.
	mu       sync.Mutex
	reserved map[int]struct{}
}

func New(cfg Config, st store.Store, n *hashistack.Nomad, b *hashistack.Boundary) *Service {
	return &Service{cfg: cfg, store: st, nomad: n, bndry: b, reserved: map[int]struct{}{}}
}

// reservePort reads the live Nomad ports, then reserves a free one under the
// lock. The returned release must be called (defer) when Create finishes: on
// success the port is by then published to Nomad and covered by UsedPorts; on
// failure releasing returns it to the free pool.
func (s *Service) reservePort() (int, func(), error) {
	used, err := s.nomad.UsedPorts()
	if err != nil {
		return 0, nil, err
	}
	return s.reserve(used)
}

// reserve is the race-critical section: the reserved set is unioned with the
// (possibly stale) used snapshot under mu so concurrent callers sharing that
// snapshot never pick the same port.
func (s *Service) reserve(used []int) (int, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.reserved {
		used = append(used, p)
	}
	port, err := portgen.Allocate(s.cfg.PortRange, used)
	if err != nil {
		return 0, nil, err
	}
	s.reserved[port] = struct{}{}
	return port, func() {
		s.mu.Lock()
		delete(s.reserved, port)
		s.mu.Unlock()
	}, nil
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
	// SharedVolumes are the names of this project's shared volumes the developer
	// chose to mount (pre-selected in the UI). Each is looked up in THIS project, so
	// a name from another project simply isn't found — the isolation boundary holds.
	SharedVolumes []string
}

// Workspace is the UI view of one workspace, including ready-to-use connection
// configs and the flavor's feature list.
type Workspace struct {
	Name                    string               `json:"name"`           // ws-<handle>-<ws>
	WorkspaceName           string               `json:"workspace_name"` // <ws>
	Project                 string               `json:"project"`        // owning project, for the cross-project view
	Flavor                  string               `json:"flavor"`         // template the workspace was created from
	Status                  string               `json:"status"`
	Port                    int                  `json:"port"`
	TargetID                string               `json:"target_id"`
	Alias                   string               `json:"alias"`
	Features                []descriptor.Feature `json:"features"`
	ProxyCommandConfig      string               `json:"proxycommand_config"`
	TransparentConfig       string               `json:"transparent_config"`
	BoundaryAuthenticateCmd string               `json:"boundary_authenticate_cmd"`
	// Parameters the frontend assembles into secured-ws:// deep links so the
	// locally-installed helper can authenticate / write SSH config + open the IDE
	// (the backend is remote and can't touch the developer's machine itself).
	BoundaryAddr         string `json:"boundary_addr"`
	BoundaryAuthMethodID string `json:"boundary_auth_method_id"`
	User                 string `json:"user"`
}

// ListProjects returns the descriptors the developer's groups may access.
// Descriptors are read from the Postgres control-plane store (previously Vault
// KV); a malformed row is skipped so one bad project can't hide the rest.
func (s *Service) ListProjects(ctx context.Context, groups []string) ([]descriptor.Descriptor, error) {
	rows, err := s.store.ListProjectDescriptors(ctx)
	if err != nil {
		return nil, err
	}
	all := make([]descriptor.Descriptor, 0, len(rows))
	for _, row := range rows {
		d, err := descriptor.Parse(string(row.Descriptor))
		if err != nil {
			continue
		}
		all = append(all, d)
	}
	return descriptor.Visible(all, groups), nil
}

// GetProject reads one descriptor and enforces group access.
func (s *Service) GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error) {
	row, err := s.store.GetProjectDescriptor(ctx, name)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	d, err := descriptor.Parse(string(row.Descriptor))
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	if !d.AllowsGroups(groups) {
		return descriptor.Descriptor{}, fmt.Errorf("not a member of %q: %w", d.DevelopersGroupName, apperr.ErrForbidden)
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
		out = append(out, s.view(d, j.Name, wsName, j.Status, j.Port, targetIDs[j.Name], handle, j.Flavor))
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
// renderSharedVolumes builds the group-level volume{} defs and task-level
// volume_mount{} blocks for the developer's selected shared volumes, filling the
// ${shared_volume_defs} / ${shared_volume_mounts} placeholders. Each name is
// resolved in THIS project (a foreign or stale name errors), so a workspace can
// only ever mount volumes that belong to its own project.
func (s *Service) renderSharedVolumes(ctx context.Context, project string, names []string) (defs, mounts string, err error) {
	if len(names) == 0 {
		return "", "", nil
	}
	var db, mb strings.Builder
	for _, name := range names {
		v, err := s.store.GetSharedVolume(ctx, project, name)
		if err != nil {
			return "", "", fmt.Errorf("shared volume %q: %w", name, err)
		}
		label := "sv-" + v.Name
		fmt.Fprintf(&db, "    volume %q {\n      type            = \"csi\"\n      source          = %q\n      read_only       = %t\n      access_mode     = \"multi-node-multi-writer\"\n      attachment_mode = \"file-system\"\n    }\n", label, v.VolumeID, v.ReadOnly)
		fmt.Fprintf(&mb, "      volume_mount {\n        volume      = %q\n        destination = %q\n        read_only   = %t\n      }\n", label, v.MountPath, v.ReadOnly)
	}
	return db.String(), mb.String(), nil
}

func (s *Service) Create(ctx context.Context, d descriptor.Descriptor, in CreateInput) (Workspace, error) {
	if err := requireProvisioned(d); err != nil {
		return Workspace{}, err
	}

	// Flavor is optional when the project publishes exactly one.
	if in.Flavor == "" && len(d.Flavors) == 1 {
		in.Flavor = d.Flavors[0].Name
	}
	flavor, ok := d.Flavor(in.Flavor)
	if !ok {
		return Workspace{}, fmt.Errorf("unknown flavor %q for project %q: %w", in.Flavor, d.ProjectName, apperr.ErrBadRequest)
	}

	// The workspace identity is machine-generated — the developer names nothing.
	// A random suffix keeps the job/volume/alias names unique (no collision error).
	wsName, name, volume, err := s.allocateName(d.Namespace, in.Handle)
	if err != nil {
		return Workspace{}, err
	}

	port, release, err := s.reservePort()
	if err != nil {
		return Workspace{}, err
	}
	// Hold the port until Create returns: by then RegisterJob has published it
	// to Nomad (success) or we've bailed and it's free to reuse (failure).
	defer release()

	// The project template already carries this project's static values (baked in
	// at project-template create, pass-1); the portal fills only the per-workspace
	// placeholders (pass-2). Read from the Postgres control-plane store.
	pt, err := s.store.GetProjectTemplate(ctx, d.ProjectName, in.Flavor)
	if err != nil {
		return Workspace{}, err
	}
	svDefs, svMounts, err := s.renderSharedVolumes(ctx, d.ProjectName, in.SharedVolumes)
	if err != nil {
		return Workspace{}, err
	}
	rendered, err := jobrender.Render(pt.RenderedSource, map[string]string{
		"job_name":             name,
		"ssh_port":             strconv.Itoa(port),
		"volume_name":          volume,
		"developer_email":      in.Email,
		"git_user_name":        in.GitName,
		"shared_volume_defs":   svDefs,
		"shared_volume_mounts": svMounts,
	})
	if err != nil {
		return Workspace{}, err
	}

	if err := s.nomad.CreateHostVolume(d.Namespace, volume, flavor.NodePool); err != nil {
		return Workspace{}, err
	}
	if _, err := s.nomad.RegisterJob(d.Namespace, rendered, in.Flavor); err != nil {
		return Workspace{}, err
	}

	// The Boundary host address is no longer set at create. The external
	// Nomad→Boundary host-sync registers the workspace's current node address as the
	// single host in its host-set and refreshes it across reschedules, so the target
	// follows the workspace with no create-time IP resolution.
	alias := fmt.Sprintf("%s.%s.%s.%s", wsName, in.Handle, d.ProjectName, d.AliasSuffix)
	res, err := s.bndry.Provision(ctx, hashistack.ProvisionInput{
		ScopeID:             d.ProjectScopeID,
		Name:                name,
		HostCatalogID:       d.BoundaryHostCatalogID,
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

	return s.view(d, name, wsName, "pending", port, res.TargetID, in.Handle, in.Flavor), nil
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
		return "", fmt.Errorf("writing SSH config is disabled on this portal: %w", apperr.ErrBadRequest)
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
	return Workspace{}, fmt.Errorf("workspace %q not found: %w", jobName, apperr.ErrNotFound)
}

// requireProvisioned blocks workspace creation until the project's secret
// engines are fully set up. The workspace job template reads Vault secrets the
// engines mint (GitHub PAT, LLM key, SSH cert), so launching earlier just
// leaves the alloc blocked in "pending" on vault.read with no visible error —
// fail fast here with a message naming the missing setup instead. Both flags
// come from the project descriptor: CredentialLibraryID is written only after
// engine provisioning completes, GithubConfigured only after the project-admin
// submits the GitHub App credentials (the one manual step).
func requireProvisioned(d descriptor.Descriptor) error {
	if d.CredentialLibraryID == "" {
		return fmt.Errorf("project %q is not fully provisioned — its secret engines and Boundary access are not set up yet; ask a project admin to complete project setup: %w", d.ProjectName, apperr.ErrConflict)
	}
	if !d.GithubConfigured {
		return fmt.Errorf("project %q has no GitHub access configured — a project admin must submit the GitHub App credentials on the project's \"github access\" page before workspaces can start: %w", d.ProjectName, apperr.ErrConflict)
	}
	return nil
}

// requireOwned guards that jobName belongs to handle (named ws-<handle>-...), so
// a developer can only act on their own workspaces.
func requireOwned(handle, jobName string) error {
	if !strings.HasPrefix(jobName, "ws-"+handle+"-") {
		return fmt.Errorf("workspace %q does not belong to you: %w", jobName, apperr.ErrForbidden)
	}
	return nil
}

// view assembles the UI workspace, including both connection configs. When the
// flavor (template) is known, its features are attributed exactly; otherwise we
// fall back to the single-flavor case (e.g. workspaces created before the
// template was stamped into the job meta).
func (s *Service) view(d descriptor.Descriptor, name, wsName, status string, port int, targetID, handle, flavor string) Workspace {
	user := d.WorkspaceUser
	if user == "" {
		user = "dev"
	}
	features := singleFlavorFeatures(d)
	if f, ok := d.Flavor(flavor); ok {
		features = f.Features
	}
	alias := fmt.Sprintf("%s.%s.%s.%s", wsName, handle, d.ProjectName, d.AliasSuffix)
	return Workspace{
		Name:                    name,
		WorkspaceName:           wsName,
		Project:                 d.ProjectName,
		Flavor:                  flavor,
		Status:                  status,
		Port:                    port,
		TargetID:                targetID,
		Alias:                   alias,
		Features:                features,
		ProxyCommandConfig:      ProxyCommandConfig(name, user, s.cfg.BoundaryPublicAddr, targetID),
		TransparentConfig:       TransparentConfig(alias, user),
		BoundaryAuthenticateCmd: BoundaryAuthenticateCmd(s.cfg.BoundaryPublicAddr, d.BoundaryOIDCAuthMethodID),
		BoundaryAddr:            s.cfg.BoundaryPublicAddr,
		BoundaryAuthMethodID:    d.BoundaryOIDCAuthMethodID,
		User:                    user,
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
	return "", "", "", fmt.Errorf("could not allocate a unique workspace name after 5 attempts: %w", apperr.ErrConflict)
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
