package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/agentjob"
	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// The per-user instance plane: a project-user browses published templates as
// cards and spins up an isolated instance of one. An instance owns its own Nomad
// job (named per-user) but reuses the template's LiteLLM key and scoped MCP token
// — per-user delegated identity is a later phase. Ownership is enforced by keying
// every lookup on the caller's subject, so one user can never resolve another's.

const (
	instanceRunning = "running"
	instanceStopped = "stopped"
)

// Card is a published template as a project-user sees it — enough to render a
// launch card, no secret or wiring detail.
type Card struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Greeting    string `json:"greeting,omitempty"`
	Model       string `json:"model,omitempty"`
	ToolCount   int    `json:"tool_count"`
}

// InstanceView is the caller's own instance of a template, with live job status.
type InstanceView struct {
	Template     string    `json:"template"`
	Status       string    `json:"status"`
	Running      bool      `json:"running"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// ListCards returns the project's published templates (membership-gated).
func (s *Service) ListCards(ctx context.Context, groups []string, project string) ([]Card, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return nil, err
	}
	rows, err := s.store.ListProjectAgentTemplates(ctx, project)
	if err != nil {
		return nil, err
	}
	out := []Card{}
	for _, r := range rows {
		if r.Status != statusPublished {
			continue
		}
		out = append(out, Card{
			Name: r.Name, Description: r.Description, Greeting: r.Greeting,
			Model: r.Model, ToolCount: cardToolCount(r),
		})
	}
	return out, nil
}

// ListInstances returns the caller's own instances with live job status.
func (s *Service) ListInstances(ctx context.Context, groups []string, project, subject string) ([]InstanceView, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListProjectAgentInstancesForOwner(ctx, project, subject)
	if err != nil {
		return nil, err
	}
	out := []InstanceView{}
	for _, in := range rows {
		running := false
		if in.Status == instanceRunning && in.JobID != "" {
			running, _ = s.nomad.JobExists(d.Namespace, in.JobID)
		}
		out = append(out, InstanceView{Template: in.Template, Status: in.Status, Running: running, LastActiveAt: in.LastActiveAt})
	}
	return out, nil
}

// EnsureInstance returns the caller's running instance of a published template,
// starting it on demand if the row is missing/stopped or its job is gone. It
// reuses the template's LLM key + scoped MCP token (same Vault KV the admin test
// job reads) — only the Nomad job is per-user. Idempotent: a live instance is
// returned as-is.
func (s *Service) EnsureInstance(ctx context.Context, groups []string, project, template, subject string) (store.ProjectAgentInstance, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectAgentInstance{}, err
	}
	if d.Namespace == "" {
		return store.ProjectAgentInstance{}, fmt.Errorf("project %q has no namespace: %w", project, apperr.ErrBadRequest)
	}
	tmpl, err := s.store.GetProjectAgentTemplate(ctx, project, template)
	if err != nil {
		return store.ProjectAgentInstance{}, err
	}
	if tmpl.Status != statusPublished {
		return store.ProjectAgentInstance{}, fmt.Errorf("template %q is not published: %w", template, apperr.ErrConflict)
	}

	if inst, err := s.store.GetProjectAgentInstance(ctx, project, template, subject); err == nil && inst.Status == instanceRunning && inst.JobID != "" {
		if ok, _ := s.nomad.JobExists(d.Namespace, inst.JobID); ok && s.probeHealth(ctx, inst.Endpoint).Passed {
			return inst, nil
		}
		// The job exists but isn't serving yet (still booting) — fall through to
		// re-register (idempotent for the deterministic job name) and wait for it.
	}

	jobName := instanceJob(project, template, subject)
	hcl := agentjob.Render(agentjob.RenderSpec{
		JobName:        jobName,
		Namespace:      d.Namespace,
		NodePool:       s.cfg.NodePool,
		Image:          s.cfg.Image,
		ServiceName:    jobName,
		Tags:           instanceTags(project, template, subject),
		VaultNamespace: d.Namespace,
		VaultRole:      wifRole,
		AgentName:      templateLLMLeaf(template),
		MCPServers:     mcpRefs(tmpl.Wiring),
		AgentYAML:      tmpl.YAMLSource,
	})
	jobID, err := s.nomad.RegisterJob(d.Namespace, hcl, "")
	if err != nil {
		return store.ProjectAgentInstance{}, err
	}
	ip, port, err := s.nomad.ResolvePlacement(d.Namespace, jobID)
	if err == nil && port == 0 {
		err = fmt.Errorf("nomad: no http host port assigned for %q", jobID)
	}
	if err != nil {
		_ = s.nomad.PurgeJob(d.Namespace, jobID)
		return store.ProjectAgentInstance{}, err
	}
	saved, err := s.store.UpsertProjectAgentInstance(ctx, store.ProjectAgentInstance{
		Project: project, Template: template, Subject: subject, Namespace: d.Namespace,
		Status: instanceRunning, JobID: jobID, Endpoint: endpoint(ip, port), LastActiveAt: time.Now(),
	})
	if err != nil {
		_ = s.nomad.PurgeJob(d.Namespace, jobID)
		return store.ProjectAgentInstance{}, err
	}
	// The container is placed but not serving yet — wait for /healthz so the caller's
	// first chat doesn't race the boot and 502 on connection-refused. The row is
	// already persisted (tracked + reapable); on timeout we surface a retryable 503
	// and leave the job booting so a retry reuses it via the serving fast-path above.
	if err := s.waitAgentReady(ctx, saved.Endpoint); err != nil {
		return store.ProjectAgentInstance{}, err
	}
	s.audit(ctx, subject, "project-agent-instance.start", project+"/"+template+"/"+subject, "ok", nil)
	return saved, nil
}

// ChatInstance starts the caller's instance if needed, proxies one chat turn to
// it, and stamps LastActiveAt so the idle reaper leaves an active session alone.
func (s *Service) ChatInstance(ctx context.Context, groups []string, project, template, subject string, body io.Reader) (*http.Response, error) {
	inst, err := s.EnsureInstance(ctx, groups, project, template, subject)
	if err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read chat body: %w", apperr.ErrBadRequest)
	}
	resp, err := s.postChat(ctx, inst.Endpoint, payload, nil)
	if err != nil {
		ip, port, rerr := s.nomad.ResolvePlacement(inst.Namespace, inst.JobID)
		if rerr != nil || port == 0 {
			return nil, err
		}
		inst.Endpoint = endpoint(ip, port)
		resp, err = s.postChat(ctx, inst.Endpoint, payload, nil)
		if err != nil {
			return nil, err
		}
	}
	inst.LastActiveAt = time.Now()
	_, _ = s.store.UpsertProjectAgentInstance(ctx, inst)
	return resp, nil
}

// DeleteInstance purges the caller's instance job and drops its row.
func (s *Service) DeleteInstance(ctx context.Context, groups []string, project, template, subject string) error {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return err
	}
	inst, err := s.store.GetProjectAgentInstance(ctx, project, template, subject)
	if err != nil {
		return err
	}
	if inst.JobID != "" {
		_ = s.nomad.PurgeJob(d.Namespace, inst.JobID)
	}
	if err := s.store.DeleteProjectAgentInstance(ctx, project, template, subject); err != nil {
		return err
	}
	s.audit(ctx, subject, "project-agent-instance.delete", project+"/"+template+"/"+subject, "ok", nil)
	return nil
}

// ReapIdle stops every running instance idle for longer than ttl: it purges the
// Nomad job and marks the row stopped (the row survives so the next chat
// re-provisions transparently). Cross-project; the stored Namespace lets it purge
// without a per-project lookup. Returns how many it stopped.
func (s *Service) ReapIdle(ctx context.Context, ttl time.Duration) (int, error) {
	insts, err := s.store.ListRunningProjectAgentInstances(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	reaped := 0
	for _, inst := range insts {
		if now.Sub(inst.LastActiveAt) <= ttl {
			continue
		}
		if inst.JobID != "" {
			_ = s.nomad.PurgeJob(inst.Namespace, inst.JobID)
		}
		inst.Status = instanceStopped
		inst.JobID = ""
		inst.Endpoint = ""
		if _, err := s.store.UpsertProjectAgentInstance(ctx, inst); err != nil {
			continue
		}
		s.audit(ctx, "reaper", "project-agent-instance.reap", inst.Project+"/"+inst.Template+"/"+inst.Subject, "ok", nil)
		reaped++
	}
	return reaped, nil
}

// cardToolCount reports how many tools the template's agent loaded, preferring the
// last /healthz probe (authoritative). Zero until the template has been tested.
func cardToolCount(t store.ProjectAgentTemplate) int {
	if t.TestResult != nil {
		return t.TestResult.ToolsDiscovered
	}
	return 0
}

// subjectHash is a short, DNS/Nomad-safe digest of the owner's email — used in the
// per-user job name so instances don't collide and no PII leaks into job ids.
func subjectHash(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])[:10]
}

func instanceJob(project, template, subject string) string {
	return "agent-" + project + "-" + template + "-" + subjectHash(subject)
}

func instanceTags(project, template, subject string) []string {
	return []string{"agent-instance", "agent.project=" + project, "agent.template=" + template, "agent.subject=" + subjectHash(subject)}
}
