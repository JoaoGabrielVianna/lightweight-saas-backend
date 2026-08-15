package connection

import (
	"context"
	"errors"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"gorm.io/gorm"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// Repository is the persistence contract the Service depends on.
//
// Note the shape of the secret: it goes in through Create and UpdateConfig, and
// comes out ONLY through OpenSecret. No method that returns a Connection — not
// Get, not List — can carry it, because the Connection type has nowhere to put
// it. That is the structural reason a listing cannot leak a credential, as
// opposed to a reviewer remembering not to add the field.
type Repository interface {
	Create(ctx context.Context, c *Connection, sealed secrets.Sealed) error
	GetByID(ctx context.Context, id string) (*Connection, error)
	List(ctx context.Context, workspaceID string, filter StatusFilter) ([]Connection, error)

	// OpenSecret returns the sealed credential for one connection. Named to be
	// conspicuous in a diff: a new call site is worth a second look.
	OpenSecret(ctx context.Context, id string) (*secrets.Sealed, error)

	UpdateConfig(ctx context.Context, id string, patch ConfigPatch, sealed *secrets.Sealed, now time.Time) (*Connection, error)
	SaveVerification(ctx context.Context, id string, report VerifyReport) (*Connection, error)
	Activate(ctx context.Context, id, workspaceID string, now time.Time) (*Connection, error)
	Retire(ctx context.Context, id string, now time.Time) (*Connection, error)
	Delete(ctx context.Context, id string) error

	// WithTx returns a Repository that writes through the given transaction.
	//
	// It exists so a mutation and its durable audit row can commit together
	// ([TD-033]). The methods above are unchanged and unaware: each already
	// wraps its read-modify-write in a transaction of its own, and bound to an
	// outer transaction that becomes a SAVEPOINT, which nests correctly and
	// rolls back with the outer.
	//
	// Activate is the case that makes this worth having: it retires the
	// incumbent AND promotes the successor, so a failed audit write must undo
	// two row updates, not one.
	//
	// [TD-033]: docs/TECH_DEBT.md#td-033
	WithTx(tx database.Tx) Repository
}

// ConfigPatch carries the editable configuration fields. Nil means "leave
// alone" — distinguishing an absent field from one set to empty is the whole
// reason these are pointers.
type ConfigPatch struct {
	Name     *string
	BaseURL  *string
	Realm    *string
	ClientID *string
}

// row is the persistence shape, unexported and never returned. It is the only
// type in the package that holds secret material.
type row struct {
	ID          string `gorm:"column:id;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id"`
	Name        string `gorm:"column:name"`
	Provider    string `gorm:"column:provider"`
	Status      string `gorm:"column:status"`
	BaseURL     string `gorm:"column:base_url"`
	Realm       string `gorm:"column:realm"`
	ClientID    string `gorm:"column:client_id"`

	SecretCiphertext []byte `gorm:"column:secret_ciphertext"`
	SecretNonce      []byte `gorm:"column:secret_nonce"`
	SecretKeyVersion int    `gorm:"column:secret_key_version"`
	SecretAlg        string `gorm:"column:secret_alg"`

	Health         string     `gorm:"column:health"`
	HealthMessage  *string    `gorm:"column:health_message"`
	AccessMode     string     `gorm:"column:access_mode"`
	LastVerifiedAt *time.Time `gorm:"column:last_verified_at"`

	CreatedAt   time.Time
	UpdatedAt   time.Time
	ActivatedAt *time.Time `gorm:"column:activated_at"`
	RetiredAt   *time.Time `gorm:"column:retired_at"`
}

// TableName pins the table. The schema is owned by
// migrations/000003_connections.up.sql; nothing here creates or alters it.
func (row) TableName() string { return "connections" }

func (r *row) toDomain() *Connection {
	message := ""
	if r.HealthMessage != nil {
		message = *r.HealthMessage
	}
	return &Connection{
		ID:             r.ID,
		WorkspaceID:    r.WorkspaceID,
		Name:           r.Name,
		Provider:       Provider(r.Provider),
		Status:         Status(r.Status),
		BaseURL:        r.BaseURL,
		Realm:          r.Realm,
		ClientID:       r.ClientID,
		Health:         Health(r.Health),
		HealthMessage:  message,
		AccessMode:     AccessMode(r.AccessMode),
		LastVerifiedAt: r.LastVerifiedAt,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		ActivatedAt:    r.ActivatedAt,
		RetiredAt:      r.RetiredAt,
	}
}

// StatusFilter selects which connections a listing returns.
type StatusFilter string

const (
	FilterAll     StatusFilter = "all"
	FilterDraft   StatusFilter = "draft"
	FilterActive  StatusFilter = "active"
	FilterRetired StatusFilter = "retired"
	defaultFilter              = FilterAll
)

// ParseStatusFilter maps the `status` query parameter to a filter.
//
// The default here is `all`, unlike workspaces, where it is `active`. A
// workspace listing hides archived rows because they are history; a connection
// listing must show drafts and retired ones, because the whole operator
// workflow is "create a draft, verify it, activate it, watch the old one
// retire" — hiding two thirds of that would make the API unusable.
func ParseStatusFilter(raw string) (StatusFilter, error) {
	switch raw {
	case "":
		return defaultFilter, nil
	case string(FilterAll), string(FilterDraft), string(FilterActive), string(FilterRetired):
		return StatusFilter(raw), nil
	default:
		return "", ErrInvalidStatusFilter
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

// rowFrom converts a domain connection to its persistence shape. The inverse
// of toDomain; the two are tested against each other so a field added to one
// and forgotten in the other fails loudly rather than silently reading empty.
//
// It does not carry secret material — that arrives separately, at the one call
// site that has it.
func rowFrom(c *Connection) *row {
	var message *string
	if c.HealthMessage != "" {
		m := c.HealthMessage
		message = &m
	}
	return &row{
		ID:             c.ID,
		WorkspaceID:    c.WorkspaceID,
		Name:           c.Name,
		Provider:       string(c.Provider),
		Status:         string(c.Status),
		BaseURL:        c.BaseURL,
		Realm:          c.Realm,
		ClientID:       c.ClientID,
		Health:         string(c.Health),
		HealthMessage:  message,
		AccessMode:     string(c.AccessMode),
		LastVerifiedAt: c.LastVerifiedAt,
		CreatedAt:      c.CreatedAt,
		UpdatedAt:      c.UpdatedAt,
		ActivatedAt:    c.ActivatedAt,
		RetiredAt:      c.RetiredAt,
	}
}

// Create inserts a connection in draft state together with its sealed secret.
// WithTx implements Repository.
func (r *PostgresRepository) WithTx(tx database.Tx) Repository {
	if tx == nil {
		return r
	}
	return &PostgresRepository{db: tx}
}

func (r *PostgresRepository) Create(ctx context.Context, c *Connection, sealed secrets.Sealed) error {
	rec := rowFrom(c)
	rec.SecretCiphertext = sealed.Ciphertext
	rec.SecretNonce = sealed.Nonce
	rec.SecretKeyVersion = sealed.KeyVersion
	rec.SecretAlg = sealed.Algorithm
	return r.db.WithContext(ctx).Create(rec).Error
}

// GetByID loads one connection. Returns (nil, nil) when nothing matches,
// matching the convention in internal/workspace and internal/user.
func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Connection, error) {
	rec, err := loadRow(r.db.WithContext(ctx), id)
	if err != nil || rec == nil {
		return nil, err
	}
	return rec.toDomain(), nil
}

// List returns a workspace's connections, ordered by name then id.
func (r *PostgresRepository) List(ctx context.Context, workspaceID string, filter StatusFilter) ([]Connection, error) {
	q := r.db.WithContext(ctx).Model(&row{}).
		Where("workspace_id = ?", workspaceID).
		Order("name ASC, id ASC")
	if filter != FilterAll {
		q = q.Where("status = ?", string(filter))
	}

	var rows []row
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]Connection, 0, len(rows))
	for i := range rows {
		out = append(out, *rows[i].toDomain())
	}
	return out, nil
}

// GetActiveByWorkspace returns the one connection a workspace routes through,
// or (nil, nil) when it has none.
//
// This is the runtime's query — the identity runtime calls it on every
// workspace-scoped request to find the provider to talk to. It is deliberately
// NOT on the Repository interface: no code inside this domain needs it, and
// widening the interface would oblige every fake to grow a method purely to
// satisfy a consumer in another package.
//
// The singular return is the schema's promise, not this function's guess: the
// partial unique index on (workspace_id) WHERE status = 'active' is what makes
// "the active connection" a well-defined phrase. Reading with Limit(1) rather
// than asserting on the row count would paper over a broken invariant; if the
// index were ever dropped, this returns the deterministic first row by id and
// the invariant test in the integration suite is what fails.
func (r *PostgresRepository) GetActiveByWorkspace(ctx context.Context, workspaceID string) (*Connection, error) {
	var found row
	err := r.db.WithContext(ctx).
		Where("workspace_id = ? AND status = ?", workspaceID, string(StatusActive)).
		Order("id ASC").
		First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return found.toDomain(), nil
}

// OpenSecret returns the sealed credential. It selects only the secret columns,
// so the query itself cannot be repurposed into a general row read.
func (r *PostgresRepository) OpenSecret(ctx context.Context, id string) (*secrets.Sealed, error) {
	var out struct {
		SecretCiphertext []byte
		SecretNonce      []byte
		SecretKeyVersion int
		SecretAlg        string
	}
	err := r.db.WithContext(ctx).Model(&row{}).
		Select("secret_ciphertext", "secret_nonce", "secret_key_version", "secret_alg").
		Where("id = ?", id).
		Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &secrets.Sealed{
		Ciphertext: out.SecretCiphertext,
		Nonce:      out.SecretNonce,
		KeyVersion: out.SecretKeyVersion,
		Algorithm:  out.SecretAlg,
	}, nil
}

// UpdateConfig applies a configuration patch to a draft connection.
//
// Guarded by `status = 'draft'` in the WHERE clause rather than by a preceding
// read, so the guard and the write are one statement and cannot interleave with
// a concurrent activation. The read-back distinguishes the outcomes:
//
//	row absent   -> (nil, nil)  -> service reports ErrNotFound
//	not a draft  -> ErrNotDraft / ErrRetired
//	otherwise    -> the updated row
//
// Changing the configuration resets the verification: the row's health refers
// to coordinates that no longer apply, and leaving it would let an operator
// activate a connection on the strength of a probe against a different
// provider.
func (r *PostgresRepository) UpdateConfig(ctx context.Context, id string, patch ConfigPatch, sealed *secrets.Sealed, now time.Time) (*Connection, error) {
	updates := map[string]any{"updated_at": now}
	if patch.Name != nil {
		updates["name"] = *patch.Name
	}
	if patch.BaseURL != nil {
		updates["base_url"] = *patch.BaseURL
	}
	if patch.Realm != nil {
		updates["realm"] = *patch.Realm
	}
	if patch.ClientID != nil {
		updates["client_id"] = *patch.ClientID
	}
	if sealed != nil {
		updates["secret_ciphertext"] = sealed.Ciphertext
		updates["secret_nonce"] = sealed.Nonce
		updates["secret_key_version"] = sealed.KeyVersion
		updates["secret_alg"] = sealed.Algorithm
	}

	// Any change to what we would probe invalidates the last probe.
	if patch.BaseURL != nil || patch.Realm != nil || patch.ClientID != nil || sealed != nil {
		updates["health"] = string(HealthUnknown)
		updates["health_message"] = nil
		updates["access_mode"] = string(AccessModeUnknown)
		updates["last_verified_at"] = nil
	}

	var out *Connection
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&row{}).
			Where("id = ? AND status = ?", id, string(StatusDraft)).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}

		current, err := loadRow(tx, id)
		if err != nil || current == nil {
			return err
		}
		if res.RowsAffected == 0 {
			if current.Status == string(StatusRetired) {
				return ErrRetired
			}
			return ErrNotDraft
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SaveVerification records a probe's verdict.
//
// Applies to any status: re-verifying an active connection is how an operator
// checks whether a provider is still reachable, and refusing that would make
// the endpoint useless exactly when it matters.
func (r *PostgresRepository) SaveVerification(ctx context.Context, id string, report VerifyReport) (*Connection, error) {
	health := HealthUnhealthy
	if report.OK {
		health = HealthHealthy
	}
	summary := report.Summary

	var out *Connection
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&row{}).Where("id = ?", id).Updates(map[string]any{
			"health":           string(health),
			"health_message":   &summary,
			"access_mode":      string(report.AccessMode),
			"last_verified_at": report.CheckedAt,
			"updated_at":       report.CheckedAt,
		})
		if res.Error != nil {
			return res.Error
		}

		current, err := loadRow(tx, id)
		if err != nil || current == nil {
			return err
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Activate promotes a draft to active and retires whatever was active before,
// in one transaction.
//
// Both halves must land together: an interleaving that retired the old
// connection without activating the new one would leave the workspace with no
// active connection at all, and the reverse would trip the partial unique
// index. The index is the authority on "one active per workspace" — this
// function's job is to make the swap atomic, not to check the invariant.
//
// A concurrent activation of a different connection in the same workspace loses
// on that index and surfaces as ErrWorkspaceHasActive rather than a driver
// error, via GORM's translated duplicate-key error.
func (r *PostgresRepository) Activate(ctx context.Context, id, workspaceID string, now time.Time) (*Connection, error) {
	var out *Connection

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Retire the incumbent first, so the index slot is free before the new
		// row claims it.
		if err := tx.Model(&row{}).
			Where("workspace_id = ? AND status = ? AND id <> ?", workspaceID, string(StatusActive), id).
			Updates(map[string]any{
				"status":     string(StatusRetired),
				"retired_at": now,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}

		res := tx.Model(&row{}).
			Where("id = ? AND status = ?", id, string(StatusDraft)).
			Updates(map[string]any{
				"status":       string(StatusActive),
				"activated_at": now,
				"updated_at":   now,
			})
		if res.Error != nil {
			if errors.Is(res.Error, gorm.ErrDuplicatedKey) {
				return ErrWorkspaceHasActive
			}
			return res.Error
		}

		current, err := loadRow(tx, id)
		if err != nil || current == nil {
			return err
		}
		if res.RowsAffected == 0 {
			// The service already checked the transition, so reaching here
			// means something changed underneath us between the check and the
			// write. Report the state as it now stands.
			switch current.Status {
			case string(StatusActive):
				return ErrAlreadyActive
			case string(StatusRetired):
				return ErrRetired
			default:
				return ErrNotVerified
			}
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Retire moves a connection to its terminal state. Idempotent at the SQL level
// via the status guard; the service decides that re-retiring is an error.
func (r *PostgresRepository) Retire(ctx context.Context, id string, now time.Time) (*Connection, error) {
	var out *Connection

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&row{}).
			Where("id = ? AND status <> ?", id, string(StatusRetired)).
			Updates(map[string]any{
				"status":     string(StatusRetired),
				"retired_at": now,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}

		current, err := loadRow(tx, id)
		if err != nil || current == nil {
			return err
		}
		if res.RowsAffected == 0 {
			return ErrRetired
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes a connection row, guarded so an active one cannot go.
//
// The guard is in the WHERE clause, not only in the service, because this is
// the one operation that destroys sealed credentials: a race that deleted the
// row a workspace is routing through would be unrecoverable, not merely wrong.
// Zero rows affected means either absent or active — the caller has already
// read the row and decided which.
func (r *PostgresRepository) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND status <> ?", id, string(StatusActive)).
		Delete(&row{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrActiveCannotDelete
	}
	return nil
}

// loadRow reads one row, returning (nil, nil) when it does not exist.
func loadRow(tx *gorm.DB, id string) (*row, error) {
	var found row
	err := tx.Where("id = ?", id).First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}
