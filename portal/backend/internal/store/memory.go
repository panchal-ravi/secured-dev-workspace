package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// Memory is an in-process Store. It backs unit tests and the initial deployment
// until the Postgres-backed store lands; state is lost on restart (the underlying
// Nomad jobs, gateway peers, and LiteLLM models survive independently).
type Memory struct {
	mu           sync.Mutex
	servers      map[string]MCPServer
	models       map[string]LLMModel
	blueprints   map[string]Blueprint         // key: id + "@" + version
	projectRoles map[string]ProjectRole       // key: project\x00subject\x00role
	projectMCP   map[string]ProjectMCPServer  // key: project\x00name
	projectDesc  map[string]ProjectDescriptor // key: project
	audit        []AuditEvent
	nextID       int64
	now          func() time.Time
}

// NewMemory builds an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		servers:      map[string]MCPServer{},
		models:       map[string]LLMModel{},
		blueprints:   map[string]Blueprint{},
		projectRoles: map[string]ProjectRole{},
		projectMCP:   map[string]ProjectMCPServer{},
		projectDesc:  map[string]ProjectDescriptor{},
		now:          time.Now,
	}
}

func (m *Memory) UpsertMCPServer(_ context.Context, s MCPServer) (MCPServer, error) {
	if s.Name == "" {
		return MCPServer{}, fmt.Errorf("store: mcp server name required: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.servers[s.Name]; ok {
		s.CreatedAt = existing.CreatedAt
		s.CreatedBy = existing.CreatedBy
	} else {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	m.servers[s.Name] = s
	return s, nil
}

func (m *Memory) GetMCPServer(_ context.Context, name string) (MCPServer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[name]
	if !ok {
		return MCPServer{}, fmt.Errorf("store: mcp server %q: %w", name, apperr.ErrNotFound)
	}
	return s, nil
}

func (m *Memory) ListMCPServers(_ context.Context) ([]MCPServer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MCPServer, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteMCPServer(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.servers[name]; !ok {
		return fmt.Errorf("store: mcp server %q: %w", name, apperr.ErrNotFound)
	}
	delete(m.servers, name)
	return nil
}

func (m *Memory) UpsertLLMModel(_ context.Context, model LLMModel) (LLMModel, error) {
	if model.Name == "" {
		return LLMModel{}, fmt.Errorf("store: llm model name required: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.models[model.Name]; ok {
		model.CreatedAt = existing.CreatedAt
		model.CreatedBy = existing.CreatedBy
	} else {
		model.CreatedAt = now
	}
	model.UpdatedAt = now
	m.models[model.Name] = model
	return model, nil
}

func (m *Memory) GetLLMModel(_ context.Context, name string) (LLMModel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[name]
	if !ok {
		return LLMModel{}, fmt.Errorf("store: llm model %q: %w", name, apperr.ErrNotFound)
	}
	return model, nil
}

func (m *Memory) ListLLMModels(_ context.Context) ([]LLMModel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LLMModel, 0, len(m.models))
	for _, model := range m.models {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteLLMModel(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[name]; !ok {
		return fmt.Errorf("store: llm model %q: %w", name, apperr.ErrNotFound)
	}
	delete(m.models, name)
	return nil
}

func bpKey(id string, version int) string { return fmt.Sprintf("%s@%d", id, version) }

func (m *Memory) UpsertBlueprint(_ context.Context, b Blueprint) (Blueprint, error) {
	if b.ID == "" || b.Version < 1 {
		return Blueprint{}, fmt.Errorf("store: blueprint id and version required: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := bpKey(b.ID, b.Version)
	if existing, ok := m.blueprints[k]; ok {
		b.CreatedAt = existing.CreatedAt
		b.CreatedBy = existing.CreatedBy
	} else {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	m.blueprints[k] = b
	return b, nil
}

func (m *Memory) GetBlueprint(_ context.Context, id string, version int) (Blueprint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blueprints[bpKey(id, version)]
	if !ok {
		return Blueprint{}, fmt.Errorf("store: blueprint %s@%d: %w", id, version, apperr.ErrNotFound)
	}
	return b, nil
}

func (m *Memory) ListBlueprints(_ context.Context) ([]Blueprint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Blueprint, 0, len(m.blueprints))
	for _, b := range m.blueprints {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

func (m *Memory) DeleteBlueprint(_ context.Context, id string, version int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := bpKey(id, version)
	if _, ok := m.blueprints[k]; !ok {
		return fmt.Errorf("store: blueprint %s@%d: %w", id, version, apperr.ErrNotFound)
	}
	delete(m.blueprints, k)
	return nil
}

func prKey(project, subject, role string) string {
	return project + "\x00" + subject + "\x00" + role
}

func (m *Memory) GrantProjectRole(_ context.Context, pr ProjectRole) (ProjectRole, error) {
	if pr.Project == "" || pr.Subject == "" || pr.Role == "" {
		return ProjectRole{}, fmt.Errorf("store: project role requires project, subject, role: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pr.GrantedAt.IsZero() {
		pr.GrantedAt = m.now()
	}
	m.projectRoles[prKey(pr.Project, pr.Subject, pr.Role)] = pr
	return pr, nil
}

func (m *Memory) RevokeProjectRole(_ context.Context, project, subject, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := prKey(project, subject, role)
	if _, ok := m.projectRoles[k]; !ok {
		return fmt.Errorf("store: project role %s/%s/%s: %w", project, subject, role, apperr.ErrNotFound)
	}
	delete(m.projectRoles, k)
	return nil
}

func (m *Memory) HasProjectRole(_ context.Context, project, subject, role string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.projectRoles[prKey(project, subject, role)]
	return ok, nil
}

func (m *Memory) ListProjectRoles(_ context.Context, project string) ([]ProjectRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectRole{}
	for _, pr := range m.projectRoles {
		if pr.Project == project {
			out = append(out, pr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out, nil
}

func (m *Memory) ProjectRolesForSubject(_ context.Context, subject string) ([]ProjectRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectRole{}
	for _, pr := range m.projectRoles {
		if pr.Subject == subject {
			out = append(out, pr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

func pmsKey(project, name string) string { return project + "\x00" + name }

func (m *Memory) UpsertProjectMCPServer(_ context.Context, s ProjectMCPServer) (ProjectMCPServer, error) {
	if s.Project == "" || s.Name == "" {
		return ProjectMCPServer{}, fmt.Errorf("store: project mcp server requires project and name: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := pmsKey(s.Project, s.Name)
	if existing, ok := m.projectMCP[k]; ok {
		s.CreatedAt, s.CreatedBy = existing.CreatedAt, existing.CreatedBy
	} else {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	m.projectMCP[k] = s
	return s, nil
}

func (m *Memory) GetProjectMCPServer(_ context.Context, project, name string) (ProjectMCPServer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.projectMCP[pmsKey(project, name)]
	if !ok {
		return ProjectMCPServer{}, fmt.Errorf("store: project mcp server %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	return s, nil
}

func (m *Memory) ListProjectMCPServers(_ context.Context, project string) ([]ProjectMCPServer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectMCPServer{}
	for _, s := range m.projectMCP {
		if s.Project == project {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteProjectMCPServer(_ context.Context, project, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := pmsKey(project, name)
	if _, ok := m.projectMCP[k]; !ok {
		return fmt.Errorf("store: project mcp server %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	delete(m.projectMCP, k)
	return nil
}

func (m *Memory) UpsertProjectDescriptor(_ context.Context, d ProjectDescriptor) (ProjectDescriptor, error) {
	if d.Project == "" {
		return ProjectDescriptor{}, fmt.Errorf("store: project descriptor project required: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.projectDesc[d.Project]; ok {
		d.CreatedAt, d.CreatedBy = existing.CreatedAt, existing.CreatedBy
	} else {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	m.projectDesc[d.Project] = d
	return d, nil
}

func (m *Memory) GetProjectDescriptor(_ context.Context, project string) (ProjectDescriptor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.projectDesc[project]
	if !ok {
		return ProjectDescriptor{}, fmt.Errorf("store: project descriptor %q: %w", project, apperr.ErrNotFound)
	}
	return d, nil
}

func (m *Memory) ListProjectDescriptors(_ context.Context) ([]ProjectDescriptor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ProjectDescriptor, 0, len(m.projectDesc))
	for _, d := range m.projectDesc {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, nil
}

func (m *Memory) DeleteProjectDescriptor(_ context.Context, project string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projectDesc[project]; !ok {
		return fmt.Errorf("store: project descriptor %q: %w", project, apperr.ErrNotFound)
	}
	delete(m.projectDesc, project)
	return nil
}

func (m *Memory) AppendAudit(_ context.Context, e AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	e.ID = m.nextID
	if e.At.IsZero() {
		e.At = m.now()
	}
	m.audit = append(m.audit, e)
	return nil
}

func (m *Memory) ListAudit(_ context.Context, limit int) ([]AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// newest first
	out := make([]AuditEvent, 0, len(m.audit))
	for i := len(m.audit) - 1; i >= 0; i-- {
		out = append(out, m.audit[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
