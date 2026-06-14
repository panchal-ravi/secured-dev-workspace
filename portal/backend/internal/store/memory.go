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
	mu      sync.Mutex
	servers map[string]MCPServer
	models  map[string]LLMModel
	audit   []AuditEvent
	nextID  int64
	now     func() time.Time
}

// NewMemory builds an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		servers: map[string]MCPServer{},
		models:  map[string]LLMModel{},
		now:     time.Now,
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
