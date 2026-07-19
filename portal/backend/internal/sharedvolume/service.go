// Package sharedvolume manages per-project shared EFS volumes: a project-admin
// creates named volumes (package/build caches, datasets) that mount into the
// project's workspaces at /shared. Each volume is one EFS access point provisioned
// via the Nomad CSI API; the access point's enforced POSIX identity + root path is
// the per-project tenant boundary. Bytes only — no secret material is handled here.
package sharedvolume

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// Store is the persistence subset the service needs.
type Store interface {
	CreateSharedVolume(ctx context.Context, v store.SharedVolume) (store.SharedVolume, error)
	GetSharedVolume(ctx context.Context, project, name string) (store.SharedVolume, error)
	ListSharedVolumes(ctx context.Context, project string) ([]store.SharedVolume, error)
	DeleteSharedVolume(ctx context.Context, project, name string) error
}

// ProjectLookup resolves a project descriptor (Nomad namespace + membership check).
type ProjectLookup interface {
	GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error)
}

// NomadClient provisions/deletes the backing EFS CSI volume (access point).
type NomadClient interface {
	CreateSharedVolume(namespace, name, fileSystemID string) error
	DeleteHostVolume(namespace, name string) error
}

// Config carries the EFS feature flag + the filesystem the CSI driver provisions on.
type Config struct {
	Enabled      bool
	FilesystemID string
}

type Service struct {
	store    Store
	projects ProjectLookup
	nomad    NomadClient
	cfg      Config
}

func New(st Store, projects ProjectLookup, nomad NomadClient, cfg Config) *Service {
	return &Service{store: st, projects: projects, nomad: nomad, cfg: cfg}
}

// nameRE constrains a volume name to a safe token used verbatim in the Nomad CSI
// volume id and the HCL volume label rendered into the job at launch.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

type CreateInput struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	ReadOnly  bool   `json:"read_only"`
}

// Create provisions a new shared volume: an EFS access point via the Nomad CSI API
// plus a control-plane row. A duplicate name is a 409. If the row write fails after
// the volume is created, the volume is best-effort removed so a failed create
// leaves nothing orphaned.
func (s *Service) Create(ctx context.Context, actor string, groups []string, project string, in CreateInput) (store.SharedVolume, error) {
	if !s.cfg.Enabled {
		return store.SharedVolume{}, fmt.Errorf("shared volumes are not enabled on this platform: %w", apperr.ErrBadRequest)
	}
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.SharedVolume{}, err
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return store.SharedVolume{}, fmt.Errorf("invalid shared volume name %q (lowercase; letters, digits, dash; 2-31 chars): %w", name, apperr.ErrBadRequest)
	}
	mountPath, err := normalizeMountPath(in.MountPath, name)
	if err != nil {
		return store.SharedVolume{}, err
	}
	if _, err := s.store.GetSharedVolume(ctx, project, name); err == nil {
		return store.SharedVolume{}, fmt.Errorf("shared volume %q already exists: %w", name, apperr.ErrConflict)
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return store.SharedVolume{}, err
	}
	volumeID := "shared-" + project + "-" + name
	if err := s.nomad.CreateSharedVolume(d.Namespace, volumeID, s.cfg.FilesystemID); err != nil {
		return store.SharedVolume{}, err
	}
	rec, err := s.store.CreateSharedVolume(ctx, store.SharedVolume{
		Project:   project,
		Name:      name,
		MountPath: mountPath,
		ReadOnly:  in.ReadOnly,
		VolumeID:  volumeID,
		CreatedBy: actor,
	})
	if err != nil {
		_ = s.nomad.DeleteHostVolume(d.Namespace, volumeID)
		return store.SharedVolume{}, err
	}
	return rec, nil
}

// List returns the project's shared volumes. Membership is enforced via GetProject;
// the route is capability-free (seeing, not doing) so a developer can populate the
// workspace-create selection.
func (s *Service) List(ctx context.Context, groups []string, project string) ([]store.SharedVolume, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return nil, err
	}
	return s.store.ListSharedVolumes(ctx, project)
}

// Delete removes the volume's EFS access point and control-plane row. The backing
// EFS data is destroyed with the access point.
func (s *Service) Delete(ctx context.Context, groups []string, project, name string) error {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return err
	}
	rec, err := s.store.GetSharedVolume(ctx, project, name)
	if err != nil {
		return err
	}
	if err := s.nomad.DeleteHostVolume(d.Namespace, rec.VolumeID); err != nil {
		return err
	}
	return s.store.DeleteSharedVolume(ctx, project, name)
}

// normalizeMountPath defaults an empty path to /shared/<name> and confines every
// mount under /shared so a shared volume can never shadow /home/dev or a system path.
func normalizeMountPath(p, name string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/shared/" + name, nil
	}
	if p != "/shared" && !strings.HasPrefix(p, "/shared/") {
		return "", fmt.Errorf("mount path must be under /shared (e.g. /shared/cache): %w", apperr.ErrBadRequest)
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("mount path must not contain '..': %w", apperr.ErrBadRequest)
	}
	return strings.TrimRight(p, "/"), nil
}
