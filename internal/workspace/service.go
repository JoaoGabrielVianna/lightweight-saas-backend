package workspace

import (
	"context"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
)

// AuditWriter persists a control-plane event inside the caller's transaction.
//
// Declared consumer-side and one method wide, the same shape every other seam
// in this codebase uses; *auditlog.Recorder satisfies it. Declaring it here
// rather than importing the audit storage package keeps the dependency pointing
// the way it should: this domain knows it must record what it did, and knows
// nothing about where.
//
// It returns an error, unlike audit.Record which absorbs one. That is the whole
// of [TD-033] in one signature: an audit row this service cannot write is a
// mutation this service must not commit.
//
// [TD-033]: docs/TECH_DEBT.md#td-033
type AuditWriter interface {
	RecordTx(ctx context.Context, tx database.Tx, e audit.Event) error
}

// Service holds the workspace business rules: validation, slug derivation,
// and the archive transition. It is the only layer that turns a public id into
// a storage id, and the only one that decides which failures are client errors.
//
// Since Slice 15 it also owns the TRANSACTION BOUNDARY for its mutations. That
// is a deliberate move of responsibility rather than an incidental one: the
// handler cannot own it (it would put SQL lifecycle in HTTP code) and the
// repository cannot own it (no repository knows about the audit table), so the
// use-case layer is the only place that can see both halves of the invariant.
type Service struct {
	repo  Repository
	tx    database.Runner
	audit AuditWriter

	// now is injectable so tests can assert on exact timestamps without
	// sleeping. Production always uses time.Now.
	now func() time.Time
}

// NewService constructs a Service. Returns nil when any collaborator is
// missing, matching identity.NewService: the caller reads nil as "this domain
// is not wired" and omits its routes, rather than mounting handlers that would
// panic.
//
// The runner and the writer are REQUIRED, not optional. A Service that fell
// back to a non-transactional path when they were absent would make the
// atomicity guarantee conditional on wiring nobody checks — and the failure
// mode would be a silently non-atomic production deployment that every test
// still passed. Missing means absent routes, which is loud.
func NewService(repo Repository, tx database.Runner, auditWriter AuditWriter) *Service {
	if repo == nil || tx == nil || auditWriter == nil {
		return nil
	}
	return &Service{repo: repo, tx: tx, audit: auditWriter, now: time.Now}
}

// CreateInput is the service-level create payload, already separated from the
// HTTP body so the service can be tested and reused without a gin.Context.
type CreateInput struct {
	Name string
	Slug string
}

// Create validates the input, derives a slug when none was given, and inserts.
//
// Validation order is deliberate: name first, because a caller who sent
// neither a name nor a slug should be told about the name (the required
// field), not about the empty slug that is merely a consequence.
func (s *Service) Create(ctx context.Context, in CreateInput, ev *audit.Event) (*Workspace, error) {
	name := normalizeName(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if len(name) > maxNameLength {
		return nil, ErrInvalidRequest
	}

	slug := NormalizeSlug(in.Slug)
	if slug == "" {
		// No slug supplied: derive one. SlugFromName can legitimately return
		// "" — a name of only punctuation or of non-Latin script has no ASCII
		// slug — and ValidateSlug then reports slug_invalid, which correctly
		// tells the caller to supply one explicitly.
		slug = SlugFromName(name)
	}
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}

	id, err := publicid.New()
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	w := &Workspace{
		ID:        id,
		Slug:      slug,
		Name:      name,
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// No pre-flight "does this slug exist" SELECT: it would be a lie under
	// concurrency, since two requests could both pass it. The unique index is
	// the authority and Create translates its violation to ErrSlugTaken.
	//
	// The insert and the audit row go in together. On the failure of EITHER,
	// PostgreSQL rolls back both: a workspace that exists with no record of who
	// created it, and a record of a creation that did not happen, are equally
	// unacceptable.
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		if err := s.repo.WithTx(tx).Create(ctx, w); err != nil {
			return err
		}
		ev.Workspace = w.PublicID()
		ev.Target = audit.Target{Kind: targetKind, ID: w.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// targetKind is the audit resource type every event in this domain names. It
// must match one of the values audit_events_resource_type_check permits.
const targetKind = "workspace"

// Get returns one workspace, archived or not, by its public id.
func (s *Service) Get(ctx context.Context, publicID string) (*Workspace, error) {
	id, err := s.resolveID(publicID)
	if err != nil {
		return nil, err
	}

	w, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, ErrNotFound
	}
	return w, nil
}

// List returns workspaces matching the raw `status` query parameter. Empty
// means active — archived workspaces are history, not the working set.
func (s *Service) List(ctx context.Context, rawStatus string) ([]Workspace, error) {
	filter, err := ParseStatusFilter(rawStatus)
	if err != nil {
		return nil, err
	}
	return s.repo.List(ctx, filter)
}

// UpdateName renames an active workspace.
//
// Name is the only mutable field in this slice. Slug is immutable by design —
// it is the stable handle other systems will reference — and status changes go
// through Archive, never through a generic patch.
func (s *Service) UpdateName(ctx context.Context, publicID, name string, ev *audit.Event) (*Workspace, error) {
	id, err := s.resolveID(publicID)
	if err != nil {
		return nil, err
	}

	name = normalizeName(name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if len(name) > maxNameLength {
		return nil, ErrInvalidRequest
	}

	var w *Workspace
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		w, err = s.repo.WithTx(tx).UpdateName(ctx, id, name, s.now().UTC())
		if err != nil {
			return err
		}
		if w == nil {
			// Not found is not a mutation, so there is nothing to be atomic
			// with. Returning it here still rolls the (empty) transaction back,
			// and no audit row is written for a rename that did not occur.
			return ErrNotFound
		}
		ev.Workspace = w.PublicID()
		ev.Target = audit.Target{Kind: targetKind, ID: w.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// Archive moves a workspace to its terminal state.
//
// Idempotent: archiving an already-archived workspace succeeds and returns it
// unchanged, so a client retrying after a timeout gets the same answer as the
// original call rather than a spurious conflict.
//
// Terminal: there is no reactivation in this slice, and the slug is never
// released, so a reference to a workspace slug can never later resolve to a
// different workspace.
func (s *Service) Archive(ctx context.Context, publicID string, ev *audit.Event) (*Workspace, error) {
	id, err := s.resolveID(publicID)
	if err != nil {
		return nil, err
	}

	var w *Workspace
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		w, err = s.repo.WithTx(tx).Archive(ctx, id, s.now().UTC())
		if err != nil {
			return err
		}
		if w == nil {
			return ErrNotFound
		}
		ev.Workspace = w.PublicID()
		ev.Target = audit.Target{Kind: targetKind, ID: w.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return w, nil
}

// resolveID converts a public id to a storage id.
//
// Every malformed or wrong-typed id collapses to ErrInvalidID — a 400 — and
// never to ErrNotFound. Reporting `conn_<uuid>` as "workspace not found" would
// both hide the client's actual bug and answer a question about whether some
// other object with that UUID exists.
//
// This runs before any query, which is the requirement: a bad prefix costs no
// database round trip.
func (s *Service) resolveID(publicID string) (string, error) {
	id, err := publicid.Parse(publicid.WorkspacePrefix, publicID)
	if err != nil {
		return "", ErrInvalidID
	}
	return id, nil
}
