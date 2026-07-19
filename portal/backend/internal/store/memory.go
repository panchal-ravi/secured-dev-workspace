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
	models       map[string]LLMModel
	projectRoles map[string]ProjectRole          // key: project\x00subject\x00role
	projectCaps  map[string]ProjectCapabilities  // key: project
	projectMCP   map[string]ProjectMCPServer     // key: project\x00name
	sharedVol    map[string]SharedVolume         // key: project\x00name
	projectAgent map[string]ProjectAgent         // key: project\x00name
	projectTmplA map[string]ProjectAgentTemplate // key: project\x00name
	projectInstA map[string]ProjectAgentInstance // key: project\x00template\x00subject
	projectDesc  map[string]ProjectDescriptor    // key: project
	baseTmpl     map[string]BaseJobTemplate      // key: name
	projectTmpl  map[string]ProjectTemplate      // key: project\x00flavor
	audit        []AuditEvent
	nextID       int64
	now          func() time.Time
}

// NewMemory builds an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		models:       map[string]LLMModel{},
		projectRoles: map[string]ProjectRole{},
		projectCaps:  map[string]ProjectCapabilities{},
		projectMCP:   map[string]ProjectMCPServer{},
		sharedVol:    map[string]SharedVolume{},
		projectAgent: map[string]ProjectAgent{},
		projectTmplA: map[string]ProjectAgentTemplate{},
		projectInstA: map[string]ProjectAgentInstance{},
		projectDesc:  map[string]ProjectDescriptor{},
		baseTmpl:     map[string]BaseJobTemplate{},
		projectTmpl:  map[string]ProjectTemplate{},
		now:          time.Now,
	}
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

func (m *Memory) CreateSharedVolume(_ context.Context, v SharedVolume) (SharedVolume, error) {
	if v.Project == "" || v.Name == "" {
		return SharedVolume{}, fmt.Errorf("store: shared volume requires project and name: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := pmsKey(v.Project, v.Name)
	if _, ok := m.sharedVol[k]; ok {
		return SharedVolume{}, fmt.Errorf("store: shared volume %s/%s already exists: %w", v.Project, v.Name, apperr.ErrConflict)
	}
	now := m.now()
	v.CreatedAt, v.UpdatedAt = now, now
	m.sharedVol[k] = v
	return v, nil
}

func (m *Memory) GetSharedVolume(_ context.Context, project, name string) (SharedVolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.sharedVol[pmsKey(project, name)]
	if !ok {
		return SharedVolume{}, fmt.Errorf("store: shared volume %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	return v, nil
}

func (m *Memory) ListSharedVolumes(_ context.Context, project string) ([]SharedVolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []SharedVolume{}
	for _, v := range m.sharedVol {
		if v.Project == project {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteSharedVolume(_ context.Context, project, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := pmsKey(project, name)
	if _, ok := m.sharedVol[k]; !ok {
		return fmt.Errorf("store: shared volume %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	delete(m.sharedVol, k)
	return nil
}

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

func (m *Memory) UpsertProjectAgent(_ context.Context, a ProjectAgent) (ProjectAgent, error) {
	if a.Project == "" || a.Name == "" {
		return ProjectAgent{}, fmt.Errorf("store: project agent requires project and name: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := pmsKey(a.Project, a.Name)
	if existing, ok := m.projectAgent[k]; ok {
		a.CreatedAt, a.CreatedBy = existing.CreatedAt, existing.CreatedBy
	} else {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	m.projectAgent[k] = a
	return a, nil
}

func (m *Memory) GetProjectAgent(_ context.Context, project, name string) (ProjectAgent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.projectAgent[pmsKey(project, name)]
	if !ok {
		return ProjectAgent{}, fmt.Errorf("store: project agent %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	return a, nil
}

func (m *Memory) ListProjectAgents(_ context.Context, project string) ([]ProjectAgent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectAgent{}
	for _, a := range m.projectAgent {
		if a.Project == project {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteProjectAgent(_ context.Context, project, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := pmsKey(project, name)
	if _, ok := m.projectAgent[k]; !ok {
		return fmt.Errorf("store: project agent %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	delete(m.projectAgent, k)
	return nil
}

func (m *Memory) UpsertProjectAgentTemplate(_ context.Context, t ProjectAgentTemplate) (ProjectAgentTemplate, error) {
	if t.Project == "" || t.Name == "" {
		return ProjectAgentTemplate{}, fmt.Errorf("store: project agent template requires project and name: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := pmsKey(t.Project, t.Name)
	if existing, ok := m.projectTmplA[k]; ok {
		t.CreatedAt, t.CreatedBy = existing.CreatedAt, existing.CreatedBy
	} else {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	m.projectTmplA[k] = t
	return t, nil
}

func (m *Memory) GetProjectAgentTemplate(_ context.Context, project, name string) (ProjectAgentTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.projectTmplA[pmsKey(project, name)]
	if !ok {
		return ProjectAgentTemplate{}, fmt.Errorf("store: project agent template %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	return t, nil
}

func (m *Memory) ListProjectAgentTemplates(_ context.Context, project string) ([]ProjectAgentTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectAgentTemplate{}
	for _, t := range m.projectTmplA {
		if t.Project == project {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteProjectAgentTemplate(_ context.Context, project, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := pmsKey(project, name)
	if _, ok := m.projectTmplA[k]; !ok {
		return fmt.Errorf("store: project agent template %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	delete(m.projectTmplA, k)
	return nil
}

func instKey(project, template, subject string) string {
	return project + "\x00" + template + "\x00" + subject
}

func (m *Memory) UpsertProjectAgentInstance(_ context.Context, in ProjectAgentInstance) (ProjectAgentInstance, error) {
	if in.Project == "" || in.Template == "" || in.Subject == "" {
		return ProjectAgentInstance{}, fmt.Errorf("store: project agent instance requires project, template, subject: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := instKey(in.Project, in.Template, in.Subject)
	if existing, ok := m.projectInstA[k]; ok {
		in.CreatedAt = existing.CreatedAt
	} else {
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	m.projectInstA[k] = in
	return in, nil
}

func (m *Memory) GetProjectAgentInstance(_ context.Context, project, template, subject string) (ProjectAgentInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.projectInstA[instKey(project, template, subject)]
	if !ok {
		return ProjectAgentInstance{}, fmt.Errorf("store: project agent instance %s/%s/%s: %w", project, template, subject, apperr.ErrNotFound)
	}
	return in, nil
}

func (m *Memory) ListProjectAgentInstancesForOwner(_ context.Context, project, subject string) ([]ProjectAgentInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectAgentInstance{}
	for _, in := range m.projectInstA {
		if in.Project == project && in.Subject == subject {
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Template < out[j].Template })
	return out, nil
}

func (m *Memory) ListRunningProjectAgentInstances(_ context.Context) ([]ProjectAgentInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectAgentInstance{}
	for _, in := range m.projectInstA {
		if in.Status == "running" {
			out = append(out, in)
		}
	}
	return out, nil
}

func (m *Memory) CountProjectAgentInstances(_ context.Context, project, template string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, in := range m.projectInstA {
		if in.Project == project && in.Template == template {
			n++
		}
	}
	return n, nil
}

func (m *Memory) DeleteProjectAgentInstance(_ context.Context, project, template, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := instKey(project, template, subject)
	if _, ok := m.projectInstA[k]; !ok {
		return fmt.Errorf("store: project agent instance %s/%s/%s: %w", project, template, subject, apperr.ErrNotFound)
	}
	delete(m.projectInstA, k)
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

func (m *Memory) UpsertProjectCapabilities(_ context.Context, pc ProjectCapabilities) (ProjectCapabilities, error) {
	if pc.Project == "" {
		return ProjectCapabilities{}, fmt.Errorf("store: project capabilities project required: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.projectCaps[pc.Project]; ok {
		pc.CreatedAt, pc.CreatedBy = existing.CreatedAt, existing.CreatedBy
	} else {
		pc.CreatedAt = now
	}
	pc.UpdatedAt = now
	m.projectCaps[pc.Project] = pc
	return pc, nil
}

func (m *Memory) GetProjectCapabilities(_ context.Context, project string) (ProjectCapabilities, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pc, ok := m.projectCaps[project]
	if !ok {
		return ProjectCapabilities{}, fmt.Errorf("store: project capabilities %q: %w", project, apperr.ErrNotFound)
	}
	return pc, nil
}

// DeleteProjectCapabilities is idempotent: most projects never store a matrix,
// so project-delete cleanup must not fail on a missing row.
func (m *Memory) DeleteProjectCapabilities(_ context.Context, project string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.projectCaps, project)
	return nil
}

func (m *Memory) UpsertBaseJobTemplate(_ context.Context, t BaseJobTemplate) (BaseJobTemplate, error) {
	if t.Name == "" {
		return BaseJobTemplate{}, fmt.Errorf("store: base job template name required: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.baseTmpl[t.Name]; ok {
		t.CreatedAt, t.CreatedBy = existing.CreatedAt, existing.CreatedBy
	} else {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	m.baseTmpl[t.Name] = t
	return t, nil
}

func (m *Memory) GetBaseJobTemplate(_ context.Context, name string) (BaseJobTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.baseTmpl[name]
	if !ok {
		return BaseJobTemplate{}, fmt.Errorf("store: base job template %q: %w", name, apperr.ErrNotFound)
	}
	return t, nil
}

func (m *Memory) ListBaseJobTemplates(_ context.Context) ([]BaseJobTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BaseJobTemplate, 0, len(m.baseTmpl))
	for _, t := range m.baseTmpl {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteBaseJobTemplate(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.baseTmpl[name]; !ok {
		return fmt.Errorf("store: base job template %q: %w", name, apperr.ErrNotFound)
	}
	delete(m.baseTmpl, name)
	return nil
}

func (m *Memory) UpsertProjectTemplate(_ context.Context, t ProjectTemplate) (ProjectTemplate, error) {
	if t.Project == "" || t.Flavor == "" {
		return ProjectTemplate{}, fmt.Errorf("store: project template requires project and flavor: %w", apperr.ErrBadRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := pmsKey(t.Project, t.Flavor)
	if existing, ok := m.projectTmpl[k]; ok {
		t.CreatedAt, t.CreatedBy = existing.CreatedAt, existing.CreatedBy
	} else {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	m.projectTmpl[k] = t
	return t, nil
}

func (m *Memory) GetProjectTemplate(_ context.Context, project, flavor string) (ProjectTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.projectTmpl[pmsKey(project, flavor)]
	if !ok {
		return ProjectTemplate{}, fmt.Errorf("store: project template %s/%s: %w", project, flavor, apperr.ErrNotFound)
	}
	return t, nil
}

func (m *Memory) ListProjectTemplates(_ context.Context, project string) ([]ProjectTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProjectTemplate{}
	for _, t := range m.projectTmpl {
		if t.Project == project {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Flavor < out[j].Flavor })
	return out, nil
}

func (m *Memory) DeleteProjectTemplate(_ context.Context, project, flavor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := pmsKey(project, flavor)
	if _, ok := m.projectTmpl[k]; !ok {
		return fmt.Errorf("store: project template %s/%s: %w", project, flavor, apperr.ErrNotFound)
	}
	delete(m.projectTmpl, k)
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
