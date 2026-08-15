package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// Store is the persistence boundary.
//
// An interface rather than the concrete repository, because three things have
// to be testable without PostgreSQL and are otherwise untestable at all: what
// the API does when the store is UNAVAILABLE, what a mutation does when its
// audit write FAILS, and that no secret survives the mapping. Each of those is
// a behaviour under failure, and a failure a test cannot cause is a behaviour
// nobody has verified.
//
// Deliberately three methods. A Store that grew a `Count` would invite an
// offset paginator; one that grew a `Get` would invite a per-event endpoint
// that nothing needs.
type Store interface {
	// Record persists one event. Returns an error the caller must decide about
	// — see the failure policy in recorder.go.
	Record(ctx context.Context, r Record) error

	// List returns one page, newest first.
	List(ctx context.Context, q Query) (Page, error)

	// DeleteOlderThan removes events that fell out of retention and reports how
	// many. Strictly `occurred_at < cutoff`, so an event exactly ON the cutoff
	// survives — see the retention notes in service.go.
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)

	// WithTx returns a Store that writes through the given transaction.
	//
	// This is what lets a control-plane mutation and its audit row commit
	// together ([TD-033]): the domain service opens one transaction, binds both
	// its own repository and this one to it, and PostgreSQL decides whether
	// both rows exist or neither does.
	//
	// It returns a Store rather than *Repository so the SQL has exactly one
	// implementation. The alternative — a second `RecordTx` method with its own
	// statement — would be two copies of the insert, and the copy is the one
	// that would not get the column added to it.
	//
	// [TD-033]: docs/TECH_DEBT.md#td-033
	WithTx(tx database.Tx) Store
}

// Repository is the PostgreSQL Store.
type Repository struct {
	db *gorm.DB
}

// NewRepository constructs the store. Returns nil for a nil handle so the
// composition root omits the audit surface rather than wiring something that
// panics on first use — the same shape SetupConnection uses for a missing key.
func NewRepository(db *gorm.DB) *Repository {
	if db == nil {
		return nil
	}
	return &Repository{db: db}
}

// auditEventRow is the table mapping. Kept unexported and separate from Record
// so the wire/domain type is not shaped by column names, and so nullability is
// explicit here rather than implied by empty strings everywhere else.
type auditEventRow struct {
	ID          string  `gorm:"column:id;primaryKey"`
	WorkspaceID *string `gorm:"column:workspace_id"`

	EventType string `gorm:"column:event_type"`
	Outcome   string `gorm:"column:outcome"`

	ActorType         string  `gorm:"column:actor_type"`
	ActorSubject      *string `gorm:"column:actor_subject"`
	ActorEmail        *string `gorm:"column:actor_email"`
	ActorProjectID    *string `gorm:"column:actor_project_id"`
	ActorCredentialID *string `gorm:"column:actor_credential_id"`

	ResourceType *string `gorm:"column:resource_type"`
	ResourceID   *string `gorm:"column:resource_id"`

	RequestID *string `gorm:"column:request_id"`
	SourceIP  *string `gorm:"column:source_ip"`

	ReasonCode *string `gorm:"column:reason_code"`
	Metadata   []byte  `gorm:"column:metadata;type:jsonb"`

	OccurredAt time.Time `gorm:"column:occurred_at"`
}

func (auditEventRow) TableName() string { return "audit_events" }

// WithTx implements Store. The returned Repository shares nothing with the
// receiver except its SQL: it is a new value bound to the caller's transaction,
// so a concurrent request cannot observe or disturb it.
func (r *Repository) WithTx(tx database.Tx) Store {
	if tx == nil {
		return r
	}
	return &Repository{db: tx}
}

// Record inserts one event, through whichever handle this Repository is bound
// to — the pool, or a caller's transaction.
func (r *Repository) Record(ctx context.Context, rec Record) error {
	row := auditEventRow{
		ID:                rec.ID,
		WorkspaceID:       nullable(rec.WorkspaceID),
		EventType:         rec.EventType,
		Outcome:           string(rec.Outcome),
		ActorType:         string(rec.ActorType),
		ActorSubject:      nullable(rec.ActorSubject),
		ActorEmail:        nullable(rec.ActorEmail),
		ActorProjectID:    nullable(rec.ActorProjectID),
		ActorCredentialID: nullable(rec.ActorCredentialID),
		ResourceType:      nullable(string(rec.ResourceType)),
		ResourceID:        nullable(rec.ResourceID),
		RequestID:         nullable(rec.RequestID),
		SourceIP:          nullable(rec.SourceIP),
		ReasonCode:        nullable(rec.ReasonCode),
		OccurredAt:        rec.OccurredAt,
	}

	if len(rec.Metadata) > 0 {
		encoded, err := json.Marshal(rec.Metadata)
		if err != nil {
			// Cannot happen with the allowlisted scalar values the recorder
			// produces. Dropping the metadata rather than the event is the
			// right trade: the actor, verb and outcome are what the row is for.
			return fmt.Errorf("auditlog: encode metadata: %w", err)
		}
		row.Metadata = encoded
	}

	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("auditlog: record event: %w", err)
	}
	return nil
}

// List returns one page in (occurred_at DESC, id DESC) order.
//
// # How the page boundary is decided
//
// It fetches Limit+1 rows. If the extra row comes back there is more history,
// and the cursor is taken from the LAST ROW OF THE PAGE — not from the extra
// one, which is discarded. Deriving "has more" from `len(rows) == Limit`
// instead would produce a final page that advertises another and then returns
// nothing.
func (r *Repository) List(ctx context.Context, q Query) (Page, error) {
	// A query with no workspace returns nothing rather than everything.
	//
	// This is the last line of the workspace boundary, and it is here rather
	// than only in the handler because a future caller that forgets to set it
	// must get an empty page, not the whole installation's history.
	if q.WorkspaceID == "" {
		return Page{}, nil
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}

	tx := listQuery(r.db.WithContext(ctx), q)

	var rows []auditEventRow
	if err := tx.
		Order("occurred_at DESC, id DESC").
		Limit(limit + 1).
		Find(&rows).Error; err != nil {
		return Page{}, fmt.Errorf("auditlog: list events: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	page := Page{Items: make([]Record, 0, len(rows))}
	for _, row := range rows {
		rec, err := row.toRecord()
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, rec)
	}
	if hasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &Cursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return page, nil
}

// DeleteOlderThan removes events older than cutoff.
//
// Strictly `<`, so an event exactly on the boundary survives. The choice is
// arbitrary in principle and load-bearing in practice: it has to be ONE of the
// two, stated, and tested, or a retention test written against `<=` passes
// against an implementation that uses `<` for every input except the one that
// matters.
func (r *Repository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tx := r.db.WithContext(ctx).
		Where("occurred_at < ?", cutoff.UTC()).
		Delete(&auditEventRow{})
	if tx.Error != nil {
		return 0, fmt.Errorf("auditlog: delete expired events: %w", tx.Error)
	}
	return tx.RowsAffected, nil
}

// listQuery applies the workspace boundary, the filters and the cursor.
//
// Extracted so the plan test can EXPLAIN the query the repository ACTUALLY
// runs. The first version of that test hand-wrote the SQL, which meant it
// measured a query no code path issued — and it kept reporting a plan for a
// predicate the repository had already stopped using.
func listQuery(db *gorm.DB, q Query) *gorm.DB {
	tx := db.Model(&auditEventRow{}).
		Where("workspace_id = ?", q.WorkspaceID)

	if q.EventType != "" {
		tx = tx.Where("event_type = ?", q.EventType)
	}
	if q.ActorType != "" {
		tx = tx.Where("actor_type = ?", string(q.ActorType))
	}
	if q.Outcome != "" {
		tx = tx.Where("outcome = ?", string(q.Outcome))
	}
	if !q.From.IsZero() {
		tx = tx.Where("occurred_at >= ?", q.From.UTC())
	}
	if !q.To.IsZero() {
		tx = tx.Where("occurred_at <= ?", q.To.UTC())
	}
	if q.After != nil {
		// The cursor predicate is split into a RANGE part and a TIE part, and
		// the split is what makes this query scale.
		//
		// The obvious spelling is the row-value comparison
		// `(occurred_at, id) < (?, ?)`. It is correct and it was MEASURED to be
		// slow: PostgreSQL only recognises a row comparison as an index
		// condition when it starts at the index's FIRST column, and here the
		// first column is workspace_id, pinned by equality. The row compare
		// therefore became a post-index Filter, the planner preferred the
		// occurred_at-only index, and a 50-row page cost ~1000 rows scanned and
		// an Incremental Sort on a twenty-workspace dataset.
		//
		// Written as two conjuncts instead:
		//
		//	occurred_at <= t            → index condition, with workspace_id = ?,
		//	                              on the composite index
		//	occurred_at < t OR id < i   → a filter that can only discard rows
		//	                              sharing the boundary timestamp
		//
		// Same result set, and the work is bounded by the page instead of by
		// the number of tenants.
		at := q.After.OccurredAt.UTC()
		tx = tx.Where("occurred_at <= ?", at).
			Where("(occurred_at < ? OR id < ?)", at, q.After.ID)
	}
	return tx
}

func (row auditEventRow) toRecord() (Record, error) {
	rec := Record{
		ID:                row.ID,
		WorkspaceID:       deref(row.WorkspaceID),
		EventType:         row.EventType,
		Outcome:           Outcome(row.Outcome),
		ActorType:         actorType(row.ActorType),
		ActorSubject:      deref(row.ActorSubject),
		ActorEmail:        deref(row.ActorEmail),
		ActorProjectID:    deref(row.ActorProjectID),
		ActorCredentialID: deref(row.ActorCredentialID),
		ResourceType:      ResourceType(deref(row.ResourceType)),
		ResourceID:        deref(row.ResourceID),
		RequestID:         deref(row.RequestID),
		SourceIP:          deref(row.SourceIP),
		ReasonCode:        deref(row.ReasonCode),
		OccurredAt:        row.OccurredAt.UTC(),
	}
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &rec.Metadata); err != nil {
			return Record{}, fmt.Errorf("auditlog: decode metadata for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}

// nullable maps "" onto SQL NULL.
//
// Empty string and NULL must not both be storable: the constraint that keeps
// operator and project rows disjoint is expressed with IS NULL, and an empty
// string would satisfy the column while failing the check — or worse, pass it
// and make a project row look like it had a blank subject.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// errUnknownActorType guards the one row shape that must never be read back as
// something plausible.
var errUnknownActorType = errors.New("auditlog: unknown actor type")
