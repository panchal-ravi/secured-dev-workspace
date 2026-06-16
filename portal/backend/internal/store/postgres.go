package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq" // postgres driver for database/sql

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// Postgres is the durable Store backing the Platform Admin onboarding plane. It is
// a drop-in for the same interface as Memory: identity, lifecycle, and ordering
// live in promoted columns (queryable), while the full record is kept as a JSONB
// `data` blob. The columns are authoritative — on read the column values overwrite
// their counterparts in the unmarshalled blob, so a stale blob timestamp after an
// update can never be observed.
//
// Only metadata and references are stored here; secret material (provider keys,
// gateway tokens) stays in Vault/LiteLLM, never in this database.
type Postgres struct {
	db  *sql.DB
	now func() time.Time
}

// compile-time check: Postgres satisfies Store.
var _ Store = (*Postgres)(nil)

const schema = `
CREATE TABLE IF NOT EXISTS mcp_servers (
    name       TEXT PRIMARY KEY,
    status     TEXT        NOT NULL,
    version    INTEGER     NOT NULL DEFAULT 0,
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    data       JSONB       NOT NULL
);
CREATE TABLE IF NOT EXISTS llm_models (
    name       TEXT PRIMARY KEY,
    status     TEXT        NOT NULL,
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    data       JSONB       NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_events (
    id      BIGSERIAL PRIMARY KEY,
    actor   TEXT        NOT NULL,
    action  TEXT        NOT NULL,
    target  TEXT        NOT NULL,
    outcome TEXT        NOT NULL,
    detail  JSONB,
    at      TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS blueprints (
    id           TEXT        NOT NULL,
    version      INTEGER     NOT NULL,
    class        TEXT        NOT NULL,
    content_hash TEXT        NOT NULL,
    status       TEXT        NOT NULL,
    created_by   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    data         JSONB       NOT NULL,
    PRIMARY KEY (id, version)
);
CREATE TABLE IF NOT EXISTS project_roles (
    project    TEXT        NOT NULL,
    subject    TEXT        NOT NULL,
    role       TEXT        NOT NULL,
    granted_by TEXT        NOT NULL DEFAULT '',
    granted_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project, subject, role)
);
`

// NewPostgres opens the portal control-plane database, verifies connectivity, and
// applies the (idempotent) schema. The DSN is a standard libpq connection string
// (e.g. the one rendered from Vault dynamic DB creds for the portal-postgres job).
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	p := &Postgres{db: db, now: time.Now}
	if err := p.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return p, nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	if err := p.db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping postgres: %w", err)
	}
	if _, err := p.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("store: apply schema: %w", err)
	}
	return nil
}

// Close releases the underlying connection pool.
func (p *Postgres) Close() error { return p.db.Close() }

// ---- MCP servers ----

func (p *Postgres) UpsertMCPServer(ctx context.Context, s MCPServer) (MCPServer, error) {
	if s.Name == "" {
		return MCPServer{}, fmt.Errorf("store: mcp server name required: %w", apperr.ErrBadRequest)
	}
	now := p.now()
	s.CreatedAt, s.UpdatedAt = now, now
	blob, err := json.Marshal(s)
	if err != nil {
		return MCPServer{}, fmt.Errorf("store: marshal mcp server: %w", err)
	}
	// created_by / created_at are absent from the UPDATE set, so a conflict
	// preserves the original; RETURNING hands back the authoritative values.
	const q = `
INSERT INTO mcp_servers (name, status, version, created_by, created_at, updated_at, data)
VALUES ($1, $2, $3, $4, $5, $5, $6)
ON CONFLICT (name) DO UPDATE SET
    status     = EXCLUDED.status,
    version    = EXCLUDED.version,
    updated_at = EXCLUDED.updated_at,
    data       = EXCLUDED.data
RETURNING created_by, created_at, updated_at`
	row := p.db.QueryRowContext(ctx, q, s.Name, s.Status, s.Version, s.CreatedBy, now, blob)
	if err := row.Scan(&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return MCPServer{}, fmt.Errorf("store: upsert mcp server: %w", err)
	}
	return s, nil
}

func (p *Postgres) GetMCPServer(ctx context.Context, name string) (MCPServer, error) {
	const q = `SELECT data, status, version, created_by, created_at, updated_at FROM mcp_servers WHERE name = $1`
	var (
		s         MCPServer
		blob      []byte
		status    string
		version   int
		createdBy string
		createdAt time.Time
		updatedAt time.Time
	)
	err := p.db.QueryRowContext(ctx, q, name).Scan(&blob, &status, &version, &createdBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPServer{}, fmt.Errorf("store: mcp server %q: %w", name, apperr.ErrNotFound)
	}
	if err != nil {
		return MCPServer{}, fmt.Errorf("store: get mcp server: %w", err)
	}
	if err := json.Unmarshal(blob, &s); err != nil {
		return MCPServer{}, fmt.Errorf("store: unmarshal mcp server: %w", err)
	}
	s.Status, s.Version, s.CreatedBy, s.CreatedAt, s.UpdatedAt = status, version, createdBy, createdAt, updatedAt
	return s, nil
}

func (p *Postgres) ListMCPServers(ctx context.Context) ([]MCPServer, error) {
	const q = `SELECT data, status, version, created_by, created_at, updated_at FROM mcp_servers ORDER BY name`
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list mcp servers: %w", err)
	}
	defer rows.Close()
	out := []MCPServer{}
	for rows.Next() {
		var (
			s         MCPServer
			blob      []byte
			status    string
			version   int
			createdBy string
			createdAt time.Time
			updatedAt time.Time
		)
		if err := rows.Scan(&blob, &status, &version, &createdBy, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("store: scan mcp server: %w", err)
		}
		if err := json.Unmarshal(blob, &s); err != nil {
			return nil, fmt.Errorf("store: unmarshal mcp server: %w", err)
		}
		s.Status, s.Version, s.CreatedBy, s.CreatedAt, s.UpdatedAt = status, version, createdBy, createdAt, updatedAt
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteMCPServer(ctx context.Context, name string) error {
	return p.deleteByName(ctx, "mcp_servers", "mcp server", name)
}

// ---- LLM models ----

func (p *Postgres) UpsertLLMModel(ctx context.Context, m LLMModel) (LLMModel, error) {
	if m.Name == "" {
		return LLMModel{}, fmt.Errorf("store: llm model name required: %w", apperr.ErrBadRequest)
	}
	now := p.now()
	m.CreatedAt, m.UpdatedAt = now, now
	blob, err := json.Marshal(m)
	if err != nil {
		return LLMModel{}, fmt.Errorf("store: marshal llm model: %w", err)
	}
	const q = `
INSERT INTO llm_models (name, status, created_by, created_at, updated_at, data)
VALUES ($1, $2, $3, $4, $4, $5)
ON CONFLICT (name) DO UPDATE SET
    status     = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at,
    data       = EXCLUDED.data
RETURNING created_by, created_at, updated_at`
	row := p.db.QueryRowContext(ctx, q, m.Name, m.Status, m.CreatedBy, now, blob)
	if err := row.Scan(&m.CreatedBy, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return LLMModel{}, fmt.Errorf("store: upsert llm model: %w", err)
	}
	return m, nil
}

func (p *Postgres) GetLLMModel(ctx context.Context, name string) (LLMModel, error) {
	const q = `SELECT data, status, created_by, created_at, updated_at FROM llm_models WHERE name = $1`
	var (
		m         LLMModel
		blob      []byte
		status    string
		createdBy string
		createdAt time.Time
		updatedAt time.Time
	)
	err := p.db.QueryRowContext(ctx, q, name).Scan(&blob, &status, &createdBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LLMModel{}, fmt.Errorf("store: llm model %q: %w", name, apperr.ErrNotFound)
	}
	if err != nil {
		return LLMModel{}, fmt.Errorf("store: get llm model: %w", err)
	}
	if err := json.Unmarshal(blob, &m); err != nil {
		return LLMModel{}, fmt.Errorf("store: unmarshal llm model: %w", err)
	}
	m.Status, m.CreatedBy, m.CreatedAt, m.UpdatedAt = status, createdBy, createdAt, updatedAt
	return m, nil
}

func (p *Postgres) ListLLMModels(ctx context.Context) ([]LLMModel, error) {
	const q = `SELECT data, status, created_by, created_at, updated_at FROM llm_models ORDER BY name`
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list llm models: %w", err)
	}
	defer rows.Close()
	out := []LLMModel{}
	for rows.Next() {
		var (
			m         LLMModel
			blob      []byte
			status    string
			createdBy string
			createdAt time.Time
			updatedAt time.Time
		)
		if err := rows.Scan(&blob, &status, &createdBy, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("store: scan llm model: %w", err)
		}
		if err := json.Unmarshal(blob, &m); err != nil {
			return nil, fmt.Errorf("store: unmarshal llm model: %w", err)
		}
		m.Status, m.CreatedBy, m.CreatedAt, m.UpdatedAt = status, createdBy, createdAt, updatedAt
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteLLMModel(ctx context.Context, name string) error {
	return p.deleteByName(ctx, "llm_models", "llm model", name)
}

// ---- blueprints ----

func (p *Postgres) UpsertBlueprint(ctx context.Context, b Blueprint) (Blueprint, error) {
	if b.ID == "" || b.Version < 1 {
		return Blueprint{}, fmt.Errorf("store: blueprint id and version required: %w", apperr.ErrBadRequest)
	}
	now := p.now()
	b.CreatedAt, b.UpdatedAt = now, now
	blob, err := json.Marshal(b)
	if err != nil {
		return Blueprint{}, fmt.Errorf("store: marshal blueprint: %w", err)
	}
	const q = `
INSERT INTO blueprints (id, version, class, content_hash, status, created_by, created_at, updated_at, data)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8)
ON CONFLICT (id, version) DO UPDATE SET
    class        = EXCLUDED.class,
    content_hash = EXCLUDED.content_hash,
    status       = EXCLUDED.status,
    updated_at   = EXCLUDED.updated_at,
    data         = EXCLUDED.data
RETURNING created_by, created_at, updated_at`
	row := p.db.QueryRowContext(ctx, q, b.ID, b.Version, b.Class, b.ContentHash, b.Status, b.CreatedBy, now, blob)
	if err := row.Scan(&b.CreatedBy, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return Blueprint{}, fmt.Errorf("store: upsert blueprint: %w", err)
	}
	return b, nil
}

func (p *Postgres) GetBlueprint(ctx context.Context, id string, version int) (Blueprint, error) {
	const q = `SELECT data FROM blueprints WHERE id = $1 AND version = $2`
	var blob []byte
	switch err := p.db.QueryRowContext(ctx, q, id, version).Scan(&blob); {
	case errors.Is(err, sql.ErrNoRows):
		return Blueprint{}, fmt.Errorf("store: blueprint %s@%d: %w", id, version, apperr.ErrNotFound)
	case err != nil:
		return Blueprint{}, fmt.Errorf("store: get blueprint: %w", err)
	}
	var b Blueprint
	if err := json.Unmarshal(blob, &b); err != nil {
		return Blueprint{}, fmt.Errorf("store: unmarshal blueprint: %w", err)
	}
	return b, nil
}

func (p *Postgres) ListBlueprints(ctx context.Context) ([]Blueprint, error) {
	const q = `SELECT data FROM blueprints ORDER BY id, version`
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list blueprints: %w", err)
	}
	defer rows.Close()
	var out []Blueprint
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("store: scan blueprint: %w", err)
		}
		var b Blueprint
		if err := json.Unmarshal(blob, &b); err != nil {
			return nil, fmt.Errorf("store: unmarshal blueprint: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- project roles ----

func (p *Postgres) GrantProjectRole(ctx context.Context, pr ProjectRole) (ProjectRole, error) {
	if pr.Project == "" || pr.Subject == "" || pr.Role == "" {
		return ProjectRole{}, fmt.Errorf("store: project role requires project, subject, role: %w", apperr.ErrBadRequest)
	}
	if pr.GrantedAt.IsZero() {
		pr.GrantedAt = p.now()
	}
	const q = `
INSERT INTO project_roles (project, subject, role, granted_by, granted_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (project, subject, role) DO UPDATE SET
    granted_by = EXCLUDED.granted_by,
    granted_at = EXCLUDED.granted_at
RETURNING granted_at`
	if err := p.db.QueryRowContext(ctx, q, pr.Project, pr.Subject, pr.Role, pr.GrantedBy, pr.GrantedAt).Scan(&pr.GrantedAt); err != nil {
		return ProjectRole{}, fmt.Errorf("store: grant project role: %w", err)
	}
	return pr, nil
}

func (p *Postgres) RevokeProjectRole(ctx context.Context, project, subject, role string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM project_roles WHERE project = $1 AND subject = $2 AND role = $3`, project, subject, role)
	if err != nil {
		return fmt.Errorf("store: revoke project role: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: revoke project role rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: project role %s/%s/%s: %w", project, subject, role, apperr.ErrNotFound)
	}
	return nil
}

func (p *Postgres) HasProjectRole(ctx context.Context, project, subject, role string) (bool, error) {
	var exists bool
	const q = `SELECT EXISTS(SELECT 1 FROM project_roles WHERE project = $1 AND subject = $2 AND role = $3)`
	if err := p.db.QueryRowContext(ctx, q, project, subject, role).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: has project role: %w", err)
	}
	return exists, nil
}

func (p *Postgres) ListProjectRoles(ctx context.Context, project string) ([]ProjectRole, error) {
	return p.queryProjectRoles(ctx, `SELECT project, subject, role, granted_by, granted_at FROM project_roles WHERE project = $1 ORDER BY subject`, project)
}

func (p *Postgres) ProjectRolesForSubject(ctx context.Context, subject string) ([]ProjectRole, error) {
	return p.queryProjectRoles(ctx, `SELECT project, subject, role, granted_by, granted_at FROM project_roles WHERE subject = $1 ORDER BY project`, subject)
}

func (p *Postgres) queryProjectRoles(ctx context.Context, q, arg string) ([]ProjectRole, error) {
	rows, err := p.db.QueryContext(ctx, q, arg)
	if err != nil {
		return nil, fmt.Errorf("store: query project roles: %w", err)
	}
	defer rows.Close()
	out := []ProjectRole{}
	for rows.Next() {
		var pr ProjectRole
		if err := rows.Scan(&pr.Project, &pr.Subject, &pr.Role, &pr.GrantedBy, &pr.GrantedAt); err != nil {
			return nil, fmt.Errorf("store: scan project role: %w", err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// ---- audit ----

func (p *Postgres) AppendAudit(ctx context.Context, e AuditEvent) error {
	if e.At.IsZero() {
		e.At = p.now()
	}
	var detail []byte
	if e.Detail != nil {
		var err error
		if detail, err = json.Marshal(e.Detail); err != nil {
			return fmt.Errorf("store: marshal audit detail: %w", err)
		}
	}
	const q = `INSERT INTO audit_events (actor, action, target, outcome, detail, at) VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := p.db.ExecContext(ctx, q, e.Actor, e.Action, e.Target, e.Outcome, detail, e.At); err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

func (p *Postgres) ListAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	q := `SELECT id, actor, action, target, outcome, detail, at FROM audit_events ORDER BY id DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT $1`
		args = append(args, limit)
	}
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var (
			e      AuditEvent
			detail []byte
		)
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.Outcome, &detail, &e.At); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &e.Detail); err != nil {
				return nil, fmt.Errorf("store: unmarshal audit detail: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *Postgres) deleteByName(ctx context.Context, table, label, name string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("store: delete %s: %w", label, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete %s rows: %w", label, err)
	}
	if n == 0 {
		return fmt.Errorf("store: %s %q: %w", label, name, apperr.ErrNotFound)
	}
	return nil
}
