package project

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// scopeList maps a Go []string onto a PostgreSQL text[] column.
//
// Written here rather than pulling in lib/pq for one type: this module's driver
// is pgx, lib/pq is not a direct dependency, and adding a second PostgreSQL
// driver to the build for an array codec would be a large decision for a small
// need.
//
// The encoding is deliberately strict rather than clever. Scope values are
// drawn from a closed vocabulary of lowercase letters and one colon, so the
// array literal needs no quoting or escaping — and instead of escaping
// defensively, Value REFUSES anything outside that charset. A value that cannot
// be encoded safely is a bug, and failing the INSERT surfaces it immediately
// rather than writing something that round-trips wrong.
type scopeList []string

// Value renders the PostgreSQL array literal, e.g. {users:read,users:write}.
func (s scopeList) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	for _, v := range s {
		if !isSafeArrayElement(v) {
			return nil, fmt.Errorf("project: scope %q contains characters that cannot be stored", v)
		}
	}
	return "{" + strings.Join([]string(s), ",") + "}", nil
}

// Scan parses the literal back. Accepts the string and []byte forms a driver
// may hand back, and NULL.
func (s *scopeList) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}

	var raw string
	switch v := src.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("project: cannot scan %T into scopes", src)
	}

	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") || !strings.HasSuffix(raw, "}") {
		return fmt.Errorf("project: malformed scopes array literal")
	}
	inner := raw[1 : len(raw)-1]
	if inner == "" {
		*s = scopeList{}
		return nil
	}

	parts := strings.Split(inner, ",")
	out := make(scopeList, 0, len(parts))
	for _, p := range parts {
		// Quotes would mean the value carried a character the CHECK constraint
		// forbids, so encountering one means the row was written by something
		// other than this code. Refuse rather than unquote.
		p = strings.TrimSpace(p)
		if !isSafeArrayElement(p) {
			return fmt.Errorf("project: unexpected characters in stored scope")
		}
		out = append(out, p)
	}
	*s = out
	return nil
}

// isSafeArrayElement accepts exactly the charset the scope vocabulary uses.
func isSafeArrayElement(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c == ':', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// Repository is the persistence contract the Service and the Authenticator
// depend on.
//
// Declared consumer-side and kept narrow: each method is an operation something
// actually performs. It exists so the service, the handler and the authenticator
// can be tested without a database, not as a portability layer.
//
// Contract:
//   - Get* return (nil, nil) when nothing matches. Absence is not an error at
//     this layer; the service decides what it means.
//   - CreateProject translates a name collision to ErrNameTaken.
//   - No method returns a GORM type, a *gorm.DB, or a driver error verbatim.
type Repository interface {
	CreateProject(ctx context.Context, p *Project) error
	GetProject(ctx context.Context, id string) (*Project, error)
	ListProjects(ctx context.Context, workspaceID string) ([]Project, error)
	UpdateProjectName(ctx context.Context, id, name string, now time.Time) (*Project, error)
	ArchiveProject(ctx context.Context, id string, now time.Time) (*Project, error)

	CreateCredential(ctx context.Context, c *Credential) error
	ListCredentials(ctx context.Context, projectID string) ([]Credential, error)
	GetCredential(ctx context.Context, id string) (*Credential, error)
	CountActiveCredentials(ctx context.Context, projectID string) (int, error)

	// CountActiveCredentialsByProject counts live credentials for many
	// projects at once, keyed by project id.
	//
	// It exists so the listing does not issue one count per row. That is not
	// premature optimisation: the listing is the console's landing screen for
	// this surface, and an N+1 there would grow with exactly the thing an
	// operator adds most of.
	CountActiveCredentialsByProject(ctx context.Context, projectIDs []string) (map[string]int, error)
	RevokeCredential(ctx context.Context, id, revokedBy string, now time.Time) (*Credential, error)

	// FindByKeyPrefix is the authentication lookup: one indexed row fetch that
	// returns the credential AND its project together.
	//
	// Both in one call because authentication needs both — the credential's
	// hash and state, and the project's workspace binding and status — and two
	// calls would let them describe different moments.
	FindByKeyPrefix(ctx context.Context, keyPrefix string) (*Credential, *Project, error)

	// TouchLastUsed refreshes last_used_at. Best-effort by contract: its error
	// is never allowed to fail an authentication that already succeeded.
	TouchLastUsed(ctx context.Context, id string, now time.Time) error

	// WithTx returns a Repository that writes through the given transaction.
	//
	// It exists so a mutation and its durable audit row can commit together
	// ([TD-033]). The methods above are unchanged and unaware: each already
	// wraps its read-modify-write in a transaction of its own, and bound to an
	// outer transaction that becomes a SAVEPOINT, which nests correctly and
	// rolls back with the outer.
	//
	// [TD-033]: docs/TECH_DEBT.md#td-033
	WithTx(tx database.Tx) Repository
}

// projectRow is the persistence shape for projects. Unexported and never
// leaving this file, which keeps GORM's tags and error types out of the domain.
type projectRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id"`
	Name        string `gorm:"column:name"`
	Status      string `gorm:"column:status"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time `gorm:"column:archived_at"`
}

// TableName pins the table. The schema is owned by
// migrations/000005_projects.up.sql, not by this struct.
func (projectRow) TableName() string { return "projects" }

func (r *projectRow) toDomain() *Project {
	return &Project{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		Name:        r.Name,
		Status:      Status(r.Status),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		ArchivedAt:  r.ArchivedAt,
	}
}

func projectRowFrom(p *Project) *projectRow {
	return &projectRow{
		ID:          p.ID,
		WorkspaceID: p.WorkspaceID,
		Name:        p.Name,
		Status:      string(p.Status),
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		ArchivedAt:  p.ArchivedAt,
	}
}

// credentialRow is the persistence shape for credentials.
//
// Scopes is a PostgreSQL text[]. The alternative, a JSONB blob, would have put
// the known-scope CHECK constraint out of reach and let an application bug
// persist a scope that is then granted forever.
type credentialRow struct {
	ID         string `gorm:"column:id;primaryKey"`
	ProjectID  string `gorm:"column:project_id"`
	Label      string `gorm:"column:label"`
	KeyPrefix  string `gorm:"column:key_prefix"`
	KeyHash    []byte `gorm:"column:key_hash"`
	KeyHashAlg string `gorm:"column:key_hash_alg"`

	Scopes scopeList `gorm:"column:scopes;type:text[]"`

	ExpiresAt *time.Time `gorm:"column:expires_at"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`

	CreatedBy string  `gorm:"column:created_by"`
	RevokedBy *string `gorm:"column:revoked_by"`

	CreatedAt  time.Time
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
}

func (credentialRow) TableName() string { return "project_credentials" }

func (r *credentialRow) toDomain() *Credential {
	return &Credential{
		ID:         r.ID,
		ProjectID:  r.ProjectID,
		Label:      r.Label,
		KeyPrefix:  r.KeyPrefix,
		KeyHash:    r.KeyHash,
		KeyHashAlg: r.KeyHashAlg,
		Scopes:     []string(r.Scopes),
		ExpiresAt:  r.ExpiresAt,
		RevokedAt:  r.RevokedAt,
		CreatedBy:  r.CreatedBy,
		RevokedBy:  r.RevokedBy,
		CreatedAt:  r.CreatedAt,
		LastUsedAt: r.LastUsedAt,
	}
}

func credentialRowFrom(c *Credential) *credentialRow {
	return &credentialRow{
		ID:         c.ID,
		ProjectID:  c.ProjectID,
		Label:      c.Label,
		KeyPrefix:  c.KeyPrefix,
		KeyHash:    c.KeyHash,
		KeyHashAlg: c.KeyHashAlg,
		Scopes:     scopeList(c.Scopes),
		ExpiresAt:  c.ExpiresAt,
		RevokedAt:  c.RevokedAt,
		CreatedBy:  c.CreatedBy,
		RevokedBy:  c.RevokedBy,
		CreatedAt:  c.CreatedAt,
		LastUsedAt: c.LastUsedAt,
	}
}

// PostgresRepository is the GORM-backed Repository.
type PostgresRepository struct {
	db *gorm.DB
}

// NewRepository constructs a PostgresRepository over the shared connection.
func NewRepository(db *gorm.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// CreateProject inserts a project.
//
// A name collision is detected by GORM's translated error rather than by
// matching PostgreSQL's message text: the connection is opened with
// TranslateError enabled (internal/database.Connect), so SQLSTATE 23505 arrives
// as gorm.ErrDuplicatedKey. The unique index is the authority, not a preceding
// SELECT — only the index resolves two concurrent creates of the same name, and
// one of them has to lose here.
// WithTx implements Repository.
func (r *PostgresRepository) WithTx(tx database.Tx) Repository {
	if tx == nil {
		return r
	}
	return &PostgresRepository{db: tx}
}

func (r *PostgresRepository) CreateProject(ctx context.Context, p *Project) error {
	if err := r.db.WithContext(ctx).Create(projectRowFrom(p)).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrNameTaken
		}
		return err
	}
	return nil
}

// GetProject loads one project by its canonical UUID, archived or not.
func (r *PostgresRepository) GetProject(ctx context.Context, id string) (*Project, error) {
	var found projectRow
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return found.toDomain(), nil
}

// ListProjects returns every project in a workspace, active and archived,
// ordered by name then id.
//
// Archived projects are included because this listing backs a management
// screen: hiding them would leave an operator no way to confirm an archive
// happened or to find the project again. The id is a tiebreaker so the order is
// total rather than plan-dependent.
func (r *PostgresRepository) ListProjects(ctx context.Context, workspaceID string) ([]Project, error) {
	var rows []projectRow
	err := r.db.WithContext(ctx).
		Where("workspace_id = ?", workspaceID).
		Order("name ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(rows))
	for i := range rows {
		out = append(out, *rows[i].toDomain())
	}
	return out, nil
}

// UpdateProjectName renames an active project.
//
// The UPDATE is guarded by `status = 'active'` in the WHERE clause rather than
// by a preceding read, so the guard and the write are one statement and cannot
// interleave with a concurrent archive. The read-back distinguishes the three
// ways zero rows can be affected: absent, archived, or already named that.
func (r *PostgresRepository) UpdateProjectName(ctx context.Context, id, name string, now time.Time) (*Project, error) {
	var out *Project

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&projectRow{}).
			Where("id = ? AND status = ?", id, string(StatusActive)).
			Updates(map[string]any{"name": name, "updated_at": now})
		if res.Error != nil {
			if errors.Is(res.Error, gorm.ErrDuplicatedKey) {
				return ErrNameTaken
			}
			return res.Error
		}

		current, err := loadProjectRow(tx, id)
		if err != nil {
			return err
		}
		if current == nil {
			return nil // out stays nil: not found
		}
		if res.RowsAffected == 0 && current.Status == string(StatusArchived) {
			return ErrArchived
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ArchiveProject freezes an active project, and is idempotent.
//
// Same single-statement guard as UpdateProjectName: two concurrent archives
// cannot both write, and the second affects zero rows. Zero rows is therefore
// not a failure — the read-back returns the already-archived row and the caller
// sees success, which is what makes a retried request safe.
//
// Credentials are NOT touched. The authentication query reads the project's
// status alongside the credential, so archiving stops all of them atomically.
// Walking and updating each credential row would be a loop that can half-finish.
func (r *PostgresRepository) ArchiveProject(ctx context.Context, id string, now time.Time) (*Project, error) {
	var out *Project

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&projectRow{}).
			Where("id = ? AND status = ?", id, string(StatusActive)).
			Updates(map[string]any{
				"status":      string(StatusArchived),
				"archived_at": now,
				"updated_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}

		current, err := loadProjectRow(tx, id)
		if err != nil {
			return err
		}
		if current == nil {
			return nil
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateCredential inserts a credential.
func (r *PostgresRepository) CreateCredential(ctx context.Context, c *Credential) error {
	return r.db.WithContext(ctx).Create(credentialRowFrom(c)).Error
}

// ListCredentials returns a project's credentials, newest first.
//
// Revoked credentials are included: the list is the audit trail an operator
// reads when working out which key a deployment is still using, and removing
// history from it would make that impossible.
func (r *PostgresRepository) ListCredentials(ctx context.Context, projectID string) ([]Credential, error) {
	var rows []credentialRow
	err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("created_at DESC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Credential, 0, len(rows))
	for i := range rows {
		out = append(out, *rows[i].toDomain())
	}
	return out, nil
}

// GetCredential loads one credential by its canonical UUID.
func (r *PostgresRepository) GetCredential(ctx context.Context, id string) (*Credential, error) {
	var found credentialRow
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return found.toDomain(), nil
}

// CountActiveCredentials counts live credentials, for the per-project cap.
// Served by project_credentials_active_idx.
func (r *PostgresRepository) CountActiveCredentials(ctx context.Context, projectID string) (int, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&credentialRow{}).
		Where("project_id = ? AND revoked_at IS NULL", projectID).
		Count(&n).Error
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// CountActiveCredentialsByProject counts live credentials for a set of
// projects in one round trip, served by project_credentials_active_idx.
//
// Projects with no live credentials are simply absent from the map; the caller
// reads a missing key as zero, which is what Go's map access already gives.
func (r *PostgresRepository) CountActiveCredentialsByProject(ctx context.Context, projectIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(projectIDs))
	if len(projectIDs) == 0 {
		return out, nil
	}

	var rows []struct {
		ProjectID string `gorm:"column:project_id"`
		N         int    `gorm:"column:n"`
	}
	err := r.db.WithContext(ctx).Model(&credentialRow{}).
		Select("project_id, count(*) AS n").
		Where("project_id IN ? AND revoked_at IS NULL", projectIDs).
		Group("project_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ProjectID] = row.N
	}
	return out, nil
}

// RevokeCredential marks a credential revoked.
//
// Guarded by `revoked_at IS NULL` in the WHERE clause, so two concurrent
// revocations cannot both write and the second is reported as already-revoked
// rather than silently overwriting the first revoker's attribution.
func (r *PostgresRepository) RevokeCredential(ctx context.Context, id, revokedBy string, now time.Time) (*Credential, error) {
	var out *Credential

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&credentialRow{}).
			Where("id = ? AND revoked_at IS NULL", id).
			Updates(map[string]any{"revoked_at": now, "revoked_by": revokedBy})
		if res.Error != nil {
			return res.Error
		}

		var current credentialRow
		err := tx.Where("id = ?", id).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // out stays nil: not found
		}
		if err != nil {
			return err
		}
		if res.RowsAffected == 0 {
			return ErrCredentialAlreadyRevoked
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FindByKeyPrefix is the authentication lookup.
//
// One indexed fetch on the unique key_prefix, then the owning project. It
// returns (nil, nil, nil) when nothing matches — the caller still performs a
// dummy hash comparison so an unknown prefix and a wrong secret cost the same
// time.
//
// The two reads are not wrapped in a transaction. They are two point lookups by
// primary key on rows that change rarely, and the window between them is
// microseconds; a transaction would add round trips to the hottest path in the
// system to close a race whose worst outcome is authenticating against a
// project archived microseconds ago, which the very next request rejects.
func (r *PostgresRepository) FindByKeyPrefix(ctx context.Context, keyPrefix string) (*Credential, *Project, error) {
	var credRow credentialRow
	err := r.db.WithContext(ctx).Where("key_prefix = ?", keyPrefix).First(&credRow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	var projRow projectRow
	err = r.db.WithContext(ctx).Where("id = ?", credRow.ProjectID).First(&projRow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A credential whose project vanished cannot authenticate. The FK is
		// RESTRICT so this should be unreachable; treating it as "no match"
		// rather than as an error keeps the failure closed.
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	return credRow.toDomain(), projRow.toDomain(), nil
}

// TouchLastUsed refreshes last_used_at, throttled by the caller.
//
// The guard is repeated in SQL so concurrent requests for the same credential
// do not each write: whichever arrives first wins and the rest affect zero rows.
func (r *PostgresRepository) TouchLastUsed(ctx context.Context, id string, now time.Time) error {
	return r.db.WithContext(ctx).Model(&credentialRow{}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at < ?)", id, now.Add(-lastUsedThrottle)).
		Update("last_used_at", now).Error
}

func loadProjectRow(tx *gorm.DB, id string) (*projectRow, error) {
	var found projectRow
	err := tx.Where("id = ?", id).First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}
