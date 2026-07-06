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
-- LEGACY, UNROUTED (Phase F): the platform MCP catalog + blueprint planes were
-- retired — project-admins deploy MCP servers directly (project_mcp_servers).
-- The two tables below are kept only because the schema is append-only; live
-- rows are orphaned-but-harmless. Ops MAY drop them manually:
--   DROP TABLE mcp_servers; DROP TABLE blueprints;
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
CREATE TABLE IF NOT EXISTS project_mcp_servers (
    project    TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    status     TEXT        NOT NULL,
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    data       JSONB       NOT NULL,
    PRIMARY KEY (project, name)
);
CREATE TABLE IF NOT EXISTS project_descriptors (
    project    TEXT PRIMARY KEY,
    status     TEXT        NOT NULL,
    created_by TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    data       JSONB       NOT NULL
);
CREATE TABLE IF NOT EXISTS base_job_templates (
    name         TEXT PRIMARY KEY,
    status       TEXT        NOT NULL,
    version      INTEGER     NOT NULL DEFAULT 0,
    content_hash TEXT        NOT NULL DEFAULT '',
    created_by   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    data         JSONB       NOT NULL
);
CREATE TABLE IF NOT EXISTS project_templates (
    project      TEXT        NOT NULL,
    flavor       TEXT        NOT NULL,
    status       TEXT        NOT NULL,
    base_version INTEGER     NOT NULL DEFAULT 0,
    created_by   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    data         JSONB       NOT NULL,
    PRIMARY KEY (project, flavor)
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

// ---- project MCP servers ----

// scanRow is the minimal interface shared by *sql.Row and *sql.Rows.
type scanRow interface{ Scan(dest ...any) error }

func (p *Postgres) UpsertProjectMCPServer(ctx context.Context, s ProjectMCPServer) (ProjectMCPServer, error) {
	if s.Project == "" || s.Name == "" {
		return ProjectMCPServer{}, fmt.Errorf("store: project mcp server requires project and name: %w", apperr.ErrBadRequest)
	}
	now := p.now()
	s.CreatedAt, s.UpdatedAt = now, now
	blob, err := json.Marshal(s)
	if err != nil {
		return ProjectMCPServer{}, fmt.Errorf("store: marshal project mcp server: %w", err)
	}
	const q = `
INSERT INTO project_mcp_servers (project, name, status, created_by, created_at, updated_at, data)
VALUES ($1, $2, $3, $4, $5, $5, $6)
ON CONFLICT (project, name) DO UPDATE SET
    status     = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at,
    data       = EXCLUDED.data
RETURNING created_by, created_at, updated_at`
	row := p.db.QueryRowContext(ctx, q, s.Project, s.Name, s.Status, s.CreatedBy, now, blob)
	if err := row.Scan(&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return ProjectMCPServer{}, fmt.Errorf("store: upsert project mcp server: %w", err)
	}
	return s, nil
}

func (p *Postgres) GetProjectMCPServer(ctx context.Context, project, name string) (ProjectMCPServer, error) {
	const q = `SELECT data, status, created_by, created_at, updated_at FROM project_mcp_servers WHERE project = $1 AND name = $2`
	return p.scanProjectMCP(p.db.QueryRowContext(ctx, q, project, name), project, name)
}

func (p *Postgres) ListProjectMCPServers(ctx context.Context, project string) ([]ProjectMCPServer, error) {
	const q = `SELECT data, status, created_by, created_at, updated_at FROM project_mcp_servers WHERE project = $1 ORDER BY name`
	rows, err := p.db.QueryContext(ctx, q, project)
	if err != nil {
		return nil, fmt.Errorf("store: list project mcp servers: %w", err)
	}
	defer rows.Close()
	out := []ProjectMCPServer{}
	for rows.Next() {
		s, err := p.scanProjectMCPRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteProjectMCPServer(ctx context.Context, project, name string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM project_mcp_servers WHERE project = $1 AND name = $2`, project, name)
	if err != nil {
		return fmt.Errorf("store: delete project mcp server: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete project mcp server rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: project mcp server %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	return nil
}

func (p *Postgres) scanProjectMCP(row scanRow, project, name string) (ProjectMCPServer, error) {
	var (
		s         ProjectMCPServer
		blob      []byte
		status    string
		createdBy string
		createdAt time.Time
		updatedAt time.Time
	)
	err := row.Scan(&blob, &status, &createdBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectMCPServer{}, fmt.Errorf("store: project mcp server %s/%s: %w", project, name, apperr.ErrNotFound)
	}
	if err != nil {
		return ProjectMCPServer{}, fmt.Errorf("store: get project mcp server: %w", err)
	}
	if err := json.Unmarshal(blob, &s); err != nil {
		return ProjectMCPServer{}, fmt.Errorf("store: unmarshal project mcp server: %w", err)
	}
	s.Status, s.CreatedBy, s.CreatedAt, s.UpdatedAt = status, createdBy, createdAt, updatedAt
	return s, nil
}

func (p *Postgres) scanProjectMCPRow(rows *sql.Rows) (ProjectMCPServer, error) {
	return p.scanProjectMCP(rows, "", "")
}

// ---- project descriptors ----

func (p *Postgres) UpsertProjectDescriptor(ctx context.Context, d ProjectDescriptor) (ProjectDescriptor, error) {
	if d.Project == "" {
		return ProjectDescriptor{}, fmt.Errorf("store: project descriptor project required: %w", apperr.ErrBadRequest)
	}
	now := p.now()
	d.CreatedAt, d.UpdatedAt = now, now
	blob, err := json.Marshal(d)
	if err != nil {
		return ProjectDescriptor{}, fmt.Errorf("store: marshal project descriptor: %w", err)
	}
	const q = `
INSERT INTO project_descriptors (project, status, created_by, created_at, updated_at, data)
VALUES ($1, $2, $3, $4, $4, $5)
ON CONFLICT (project) DO UPDATE SET
    status     = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at,
    data       = EXCLUDED.data
RETURNING created_by, created_at, updated_at`
	row := p.db.QueryRowContext(ctx, q, d.Project, d.Status, d.CreatedBy, now, blob)
	if err := row.Scan(&d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return ProjectDescriptor{}, fmt.Errorf("store: upsert project descriptor: %w", err)
	}
	return d, nil
}

func (p *Postgres) GetProjectDescriptor(ctx context.Context, project string) (ProjectDescriptor, error) {
	const q = `SELECT data, status, created_by, created_at, updated_at FROM project_descriptors WHERE project = $1`
	return scanProjectDescriptor(p.db.QueryRowContext(ctx, q, project), project)
}

func (p *Postgres) ListProjectDescriptors(ctx context.Context) ([]ProjectDescriptor, error) {
	const q = `SELECT data, status, created_by, created_at, updated_at FROM project_descriptors ORDER BY project`
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list project descriptors: %w", err)
	}
	defer rows.Close()
	out := []ProjectDescriptor{}
	for rows.Next() {
		d, err := scanProjectDescriptor(rows, "")
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteProjectDescriptor(ctx context.Context, project string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM project_descriptors WHERE project = $1`, project)
	if err != nil {
		return fmt.Errorf("store: delete project descriptor: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete project descriptor rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: project descriptor %q: %w", project, apperr.ErrNotFound)
	}
	return nil
}

func scanProjectDescriptor(row scanRow, project string) (ProjectDescriptor, error) {
	var (
		d         ProjectDescriptor
		blob      []byte
		status    string
		createdBy string
		createdAt time.Time
		updatedAt time.Time
	)
	err := row.Scan(&blob, &status, &createdBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectDescriptor{}, fmt.Errorf("store: project descriptor %q: %w", project, apperr.ErrNotFound)
	}
	if err != nil {
		return ProjectDescriptor{}, fmt.Errorf("store: get project descriptor: %w", err)
	}
	if err := json.Unmarshal(blob, &d); err != nil {
		return ProjectDescriptor{}, fmt.Errorf("store: unmarshal project descriptor: %w", err)
	}
	d.Status, d.CreatedBy, d.CreatedAt, d.UpdatedAt = status, createdBy, createdAt, updatedAt
	return d, nil
}

// ---- base job templates ----

func (p *Postgres) UpsertBaseJobTemplate(ctx context.Context, t BaseJobTemplate) (BaseJobTemplate, error) {
	if t.Name == "" {
		return BaseJobTemplate{}, fmt.Errorf("store: base job template name required: %w", apperr.ErrBadRequest)
	}
	now := p.now()
	t.CreatedAt, t.UpdatedAt = now, now
	blob, err := json.Marshal(t)
	if err != nil {
		return BaseJobTemplate{}, fmt.Errorf("store: marshal base job template: %w", err)
	}
	const q = `
INSERT INTO base_job_templates (name, status, version, content_hash, created_by, created_at, updated_at, data)
VALUES ($1, $2, $3, $4, $5, $6, $6, $7)
ON CONFLICT (name) DO UPDATE SET
    status       = EXCLUDED.status,
    version      = EXCLUDED.version,
    content_hash = EXCLUDED.content_hash,
    updated_at   = EXCLUDED.updated_at,
    data         = EXCLUDED.data
RETURNING created_by, created_at, updated_at`
	row := p.db.QueryRowContext(ctx, q, t.Name, t.Status, t.Version, t.ContentHash, t.CreatedBy, now, blob)
	if err := row.Scan(&t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return BaseJobTemplate{}, fmt.Errorf("store: upsert base job template: %w", err)
	}
	return t, nil
}

func (p *Postgres) GetBaseJobTemplate(ctx context.Context, name string) (BaseJobTemplate, error) {
	const q = `SELECT data, status, version, content_hash, created_by, created_at, updated_at FROM base_job_templates WHERE name = $1`
	return scanBaseJobTemplate(p.db.QueryRowContext(ctx, q, name), name)
}

func (p *Postgres) ListBaseJobTemplates(ctx context.Context) ([]BaseJobTemplate, error) {
	const q = `SELECT data, status, version, content_hash, created_by, created_at, updated_at FROM base_job_templates ORDER BY name`
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list base job templates: %w", err)
	}
	defer rows.Close()
	out := []BaseJobTemplate{}
	for rows.Next() {
		t, err := scanBaseJobTemplate(rows, "")
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteBaseJobTemplate(ctx context.Context, name string) error {
	return p.deleteByName(ctx, "base_job_templates", "base job template", name)
}

func scanBaseJobTemplate(row scanRow, name string) (BaseJobTemplate, error) {
	var (
		t           BaseJobTemplate
		blob        []byte
		status      string
		version     int
		contentHash string
		createdBy   string
		createdAt   time.Time
		updatedAt   time.Time
	)
	err := row.Scan(&blob, &status, &version, &contentHash, &createdBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BaseJobTemplate{}, fmt.Errorf("store: base job template %q: %w", name, apperr.ErrNotFound)
	}
	if err != nil {
		return BaseJobTemplate{}, fmt.Errorf("store: get base job template: %w", err)
	}
	if err := json.Unmarshal(blob, &t); err != nil {
		return BaseJobTemplate{}, fmt.Errorf("store: unmarshal base job template: %w", err)
	}
	t.Status, t.Version, t.ContentHash, t.CreatedBy, t.CreatedAt, t.UpdatedAt = status, version, contentHash, createdBy, createdAt, updatedAt
	return t, nil
}

// ---- project templates ----

func (p *Postgres) UpsertProjectTemplate(ctx context.Context, t ProjectTemplate) (ProjectTemplate, error) {
	if t.Project == "" || t.Flavor == "" {
		return ProjectTemplate{}, fmt.Errorf("store: project template requires project and flavor: %w", apperr.ErrBadRequest)
	}
	now := p.now()
	t.CreatedAt, t.UpdatedAt = now, now
	blob, err := json.Marshal(t)
	if err != nil {
		return ProjectTemplate{}, fmt.Errorf("store: marshal project template: %w", err)
	}
	const q = `
INSERT INTO project_templates (project, flavor, status, base_version, created_by, created_at, updated_at, data)
VALUES ($1, $2, $3, $4, $5, $6, $6, $7)
ON CONFLICT (project, flavor) DO UPDATE SET
    status       = EXCLUDED.status,
    base_version = EXCLUDED.base_version,
    updated_at   = EXCLUDED.updated_at,
    data         = EXCLUDED.data
RETURNING created_by, created_at, updated_at`
	row := p.db.QueryRowContext(ctx, q, t.Project, t.Flavor, t.Status, t.BaseVersion, t.CreatedBy, now, blob)
	if err := row.Scan(&t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return ProjectTemplate{}, fmt.Errorf("store: upsert project template: %w", err)
	}
	return t, nil
}

func (p *Postgres) GetProjectTemplate(ctx context.Context, project, flavor string) (ProjectTemplate, error) {
	const q = `SELECT data, status, base_version, created_by, created_at, updated_at FROM project_templates WHERE project = $1 AND flavor = $2`
	return scanProjectTemplate(p.db.QueryRowContext(ctx, q, project, flavor), project, flavor)
}

func (p *Postgres) ListProjectTemplates(ctx context.Context, project string) ([]ProjectTemplate, error) {
	const q = `SELECT data, status, base_version, created_by, created_at, updated_at FROM project_templates WHERE project = $1 ORDER BY flavor`
	rows, err := p.db.QueryContext(ctx, q, project)
	if err != nil {
		return nil, fmt.Errorf("store: list project templates: %w", err)
	}
	defer rows.Close()
	out := []ProjectTemplate{}
	for rows.Next() {
		t, err := scanProjectTemplate(rows, "", "")
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteProjectTemplate(ctx context.Context, project, flavor string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM project_templates WHERE project = $1 AND flavor = $2`, project, flavor)
	if err != nil {
		return fmt.Errorf("store: delete project template: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete project template rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: project template %s/%s: %w", project, flavor, apperr.ErrNotFound)
	}
	return nil
}

func scanProjectTemplate(row scanRow, project, flavor string) (ProjectTemplate, error) {
	var (
		t           ProjectTemplate
		blob        []byte
		status      string
		baseVersion int
		createdBy   string
		createdAt   time.Time
		updatedAt   time.Time
	)
	err := row.Scan(&blob, &status, &baseVersion, &createdBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectTemplate{}, fmt.Errorf("store: project template %s/%s: %w", project, flavor, apperr.ErrNotFound)
	}
	if err != nil {
		return ProjectTemplate{}, fmt.Errorf("store: get project template: %w", err)
	}
	if err := json.Unmarshal(blob, &t); err != nil {
		return ProjectTemplate{}, fmt.Errorf("store: unmarshal project template: %w", err)
	}
	t.Status, t.BaseVersion, t.CreatedBy, t.CreatedAt, t.UpdatedAt = status, baseVersion, createdBy, createdAt, updatedAt
	return t, nil
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
