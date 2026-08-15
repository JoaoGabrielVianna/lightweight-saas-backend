package workspace

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// Repository is the persistence contract the Service depends on.
//
// Declared on the consumer side and kept narrow: five methods, each one an
// operation the service actually performs. It exists so the service and the
// HTTP layer can be tested without a database — not as a portability layer,
// and not as a place to grow generic query builders.
//
// Contract:
//   - GetByID returns (nil, nil) when no row matches, matching the convention
//     in internal/user. Absence is not an error at this layer; the service
//     decides that it means ErrNotFound.
//   - Create translates a slug collision to ErrSlugTaken. No other domain
//     error originates here.
//   - No method returns a GORM type, a *gorm.DB, or a driver error verbatim.
type Repository interface {
	Create(ctx context.Context, w *Workspace) error
	GetByID(ctx context.Context, id string) (*Workspace, error)
	List(ctx context.Context, filter StatusFilter) ([]Workspace, error)
	UpdateName(ctx context.Context, id, name string, now time.Time) (*Workspace, error)
	Archive(ctx context.Context, id string, now time.Time) (*Workspace, error)

	// WithTx returns a Repository that writes through the given transaction.
	//
	// It exists so a mutation and its durable audit row can commit together
	// ([TD-033]): the service opens one transaction and binds both this
	// repository and the audit store to it.
	//
	// The methods above are unchanged and unaware. Each one already wraps its
	// read-modify-write in a transaction of its own; bound to an outer
	// transaction that becomes a SAVEPOINT, which nests correctly and rolls
	// back with the outer. That is why this is one method rather than a
	// parallel set of `…Tx` variants — there is one implementation of every
	// statement, and the copy that would drift does not exist.
	//
	// [TD-033]: docs/TECH_DEBT.md#td-033
	WithTx(tx database.Tx) Repository
}

// row is the persistence shape. It is unexported and never leaves this file:
// every method converts to *Workspace before returning, which is what keeps
// GORM's tags, its zero-value semantics and its error types out of the
// service and the handler.
type row struct {
	ID         string `gorm:"column:id;primaryKey"`
	Slug       string `gorm:"column:slug"`
	Name       string `gorm:"column:name"`
	Status     string `gorm:"column:status"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time `gorm:"column:archived_at"`
}

// TableName pins the table. The schema is owned by
// migrations/000002_workspaces.up.sql, not by this struct — nothing here ever
// creates or alters it.
func (row) TableName() string { return "workspaces" }

func (r *row) toDomain() *Workspace {
	return &Workspace{
		ID:         r.ID,
		Slug:       r.Slug,
		Name:       r.Name,
		Status:     Status(r.Status),
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
		ArchivedAt: r.ArchivedAt,
	}
}

func rowFrom(w *Workspace) *row {
	return &row{
		ID:         w.ID,
		Slug:       w.Slug,
		Name:       w.Name,
		Status:     string(w.Status),
		CreatedAt:  w.CreatedAt,
		UpdatedAt:  w.UpdatedAt,
		ArchivedAt: w.ArchivedAt,
	}
}

// StatusFilter selects which workspaces a listing returns.
type StatusFilter string

const (
	// FilterActive is the default: archived workspaces are operational
	// history, not part of the working set.
	FilterActive StatusFilter = "active"
	// FilterArchived returns only archived workspaces.
	FilterArchived StatusFilter = "archived"
	// FilterAll returns both.
	FilterAll StatusFilter = "all"
)

// ParseStatusFilter maps the `status` query parameter to a filter. An empty
// value means the caller did not ask, which is the default (active). Anything
// else is a client error rather than a silent fallback — a typo'd
// `status=achieved` returning the active set would be actively misleading.
func ParseStatusFilter(raw string) (StatusFilter, error) {
	switch raw {
	case "":
		return FilterActive, nil
	case string(FilterActive):
		return FilterActive, nil
	case string(FilterArchived):
		return FilterArchived, nil
	case string(FilterAll):
		return FilterAll, nil
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

// Create inserts a workspace.
//
// A slug collision is detected by GORM's translated error, not by matching
// PostgreSQL's message text: the connection is opened with TranslateError
// enabled (internal/database.Connect), so SQLSTATE 23505 arrives as
// gorm.ErrDuplicatedKey. That makes the translation deterministic and
// independent of the server's locale and version, which a string match on
// "duplicate key value violates unique constraint" would not be.
//
// The check is the authority, not the preceding SELECT the service could have
// done: only the unique index resolves two concurrent creates of the same
// slug, and one of them has to lose here.
// WithTx implements Repository.
func (r *PostgresRepository) WithTx(tx database.Tx) Repository {
	if tx == nil {
		return r
	}
	return &PostgresRepository{db: tx}
}

func (r *PostgresRepository) Create(ctx context.Context, w *Workspace) error {
	if err := r.db.WithContext(ctx).Create(rowFrom(w)).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrSlugTaken
		}
		return err
	}
	return nil
}

// GetByID loads one workspace by its canonical UUID, archived or not.
// Returns (nil, nil) when nothing matches.
func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Workspace, error) {
	var found row
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return found.toDomain(), nil
}

// List returns workspaces matching the filter, ordered by name then id.
//
// The id is a tiebreaker, not decoration: two workspaces may share a name, and
// without it their relative order would be whatever the plan happened to
// produce — which would make a paginated listing (a later slice) able to skip
// or repeat rows. idx_workspaces_status_name_id covers exactly this query.
func (r *PostgresRepository) List(ctx context.Context, filter StatusFilter) ([]Workspace, error) {
	q := r.db.WithContext(ctx).Model(&row{}).Order("name ASC, id ASC")
	if filter != FilterAll {
		q = q.Where("status = ?", string(filter))
	}

	var rows []row
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]Workspace, 0, len(rows))
	for i := range rows {
		out = append(out, *rows[i].toDomain())
	}
	return out, nil
}

// UpdateName renames an active workspace and returns the stored result.
//
// The UPDATE is guarded by `status = 'active'` rather than by a preceding
// read, so the guard and the write are one statement and cannot interleave
// with a concurrent archive. The read-back that follows is what distinguishes
// the three ways zero rows can be affected:
//
//	row absent        -> (nil, nil)          -> service reports ErrNotFound
//	row archived      -> ErrArchived
//	row already named -> the row, unchanged
//
// Both statements run in one transaction so they observe the same snapshot; a
// read-back outside it could describe a state that never existed.
func (r *PostgresRepository) UpdateName(ctx context.Context, id, name string, now time.Time) (*Workspace, error) {
	var out *Workspace

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&row{}).
			Where("id = ? AND status = ?", id, string(StatusActive)).
			Updates(map[string]any{"name": name, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}

		current, err := loadRow(tx, id)
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

// Archive moves an active workspace to archived, and is idempotent.
//
// Same single-statement guard as UpdateName: `status = 'active'` in the WHERE
// clause means two concurrent archive calls cannot both write, and the second
// one affects zero rows. Zero rows affected is therefore not a failure — the
// read-back returns the already-archived row and the caller sees success. That
// is what makes a retried request safe.
//
// status and archived_at are written together in one statement, so the
// workspaces_archived_at_check constraint is never even transiently violated.
func (r *PostgresRepository) Archive(ctx context.Context, id string, now time.Time) (*Workspace, error) {
	var out *Workspace

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&row{}).
			Where("id = ? AND status = ?", id, string(StatusActive)).
			Updates(map[string]any{
				"status":      string(StatusArchived),
				"archived_at": now,
				"updated_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}

		current, err := loadRow(tx, id)
		if err != nil {
			return err
		}
		if current == nil {
			return nil // out stays nil: not found
		}
		out = current.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// loadRow reads one row inside an existing transaction, returning (nil, nil)
// when it does not exist.
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
