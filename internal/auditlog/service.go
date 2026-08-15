package auditlog

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Page sizing.
//
// 50 is a screenful in the console and a reasonable default for a script that
// forgot to ask. 200 is the ceiling: an audit page is small rows, but the cap
// exists so a client cannot ask for the whole history in one request and turn a
// bounded query into an unbounded one.
const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// Service is the read side and the retention sweep.
//
// Thin on purpose: it validates and clamps what a client sent, encodes and
// decodes the cursor, and hands a Query to the Store. There is no business
// logic in an audit trail — the events already happened.
type Service struct {
	store Store
	now   func() time.Time
}

// NewService constructs the read service. Returns nil for a nil store, so the
// composition root omits the route.
func NewService(store Store) *Service {
	if store == nil {
		return nil
	}
	return &Service{store: store, now: time.Now}
}

// ListParams is what a request asked for, before validation.
//
// WorkspaceID is NOT here. It comes from the authorized path parameter and is
// passed separately to List, so there is no field a query string could ever be
// bound into — the workspace boundary is enforced by the shape of the call, not
// by remembering to overwrite a field after binding.
type ListParams struct {
	EventType string
	ActorType string
	Outcome   string
	From      string
	To        string
	Cursor    string
	Limit     string
}

// List returns one page for a workspace.
//
// workspaceUUID is the resolved internal id of the workspace the caller is
// authorized for.
func (s *Service) List(ctx context.Context, workspaceUUID string, p ListParams) (Page, error) {
	q := Query{WorkspaceID: workspaceUUID}

	limit, err := parseLimit(p.Limit)
	if err != nil {
		return Page{}, err
	}
	q.Limit = limit

	if p.EventType != "" {
		// Not validated against a catalogue of known event types, deliberately:
		// the vocabulary grows every slice, and a filter for an event this
		// build does not emit should return nothing rather than 400. An
		// operator filtering for an event from a newer version gets an empty
		// page, which is the truthful answer for this installation.
		if len(p.EventType) > 100 {
			return Page{}, ErrInvalidFilter.WithField("event")
		}
		q.EventType = p.EventType
	}

	if p.ActorType != "" {
		switch p.ActorType {
		case "operator", "project":
			q.ActorType = actorTypeFilter(p.ActorType)
		default:
			return Page{}, ErrInvalidFilter.WithField("actor_type")
		}
	}

	if p.Outcome != "" {
		switch Outcome(p.Outcome) {
		case OutcomeSuccess, OutcomeFailure:
			q.Outcome = Outcome(p.Outcome)
		default:
			return Page{}, ErrInvalidFilter.WithField("outcome")
		}
	}

	if p.From != "" {
		t, err := parseTime(p.From)
		if err != nil {
			return Page{}, ErrInvalidFilter.WithField("from")
		}
		q.From = t
	}
	if p.To != "" {
		t, err := parseTime(p.To)
		if err != nil {
			return Page{}, ErrInvalidFilter.WithField("to")
		}
		q.To = t
	}
	// An inverted range is a client bug, and returning an empty page for it
	// would look like "no events" — which is the same answer a correct query
	// gives, and the one that sends someone looking for a data problem.
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return Page{}, ErrInvalidFilter.WithField("from")
	}

	if p.Cursor != "" {
		cursor, err := decodeCursor(p.Cursor)
		if err != nil {
			return Page{}, ErrInvalidFilter.WithField("cursor")
		}
		q.After = cursor
	}

	return s.store.List(ctx, q)
}

func actorTypeFilter(s string) auditActorType { return auditActorType(s) }

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultPageSize, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, ErrInvalidFilter.WithField("limit")
	}
	// Out of range is REFUSED, not clamped.
	//
	// Clamping is right for a field a client cannot see the effect of; a page
	// size is not that. `limit=100000` silently answered with 200 makes a
	// caller believe it has the whole history when it has a page, and that
	// belief is the bug — it stops paginating.
	if n < 1 || n > maxPageSize {
		return 0, ErrInvalidFilter.WithField("limit")
	}
	return n, nil
}

// parseTime accepts RFC 3339, which is what every timestamp on this API already
// uses on the way out.
func parseTime(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// ─── Cursor ─────────────────────────────────────────────────────────────────
//
// Opaque to the client, and base64url of `<unix-nanos>.<uuid>`.
//
// Opaque because the format is ours to change: a client that parsed it would
// pin the pagination key, and the key is the one thing likely to change if the
// ordering ever gains a column. Not encrypted — it carries a timestamp and an
// id the client is about to be shown anyway, so encryption would protect
// nothing and add a key to manage.
//
// Nanoseconds because `occurred_at` is `timestamptz` with microsecond
// resolution and RFC 3339 text would round-trip lossily at exactly the
// boundary where a cursor matters: two events in the same microsecond.

const cursorSeparator = "."

func encodeCursor(c Cursor) string {
	raw := strconv.FormatInt(c.OccurredAt.UTC().UnixNano(), 10) + cursorSeparator + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(encoded string) (*Cursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	nanos, id, found := strings.Cut(string(decoded), cursorSeparator)
	if !found {
		return nil, errBadCursor
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return nil, errBadCursor
	}
	// The id goes into a parameterised comparison, so it cannot be injection —
	// but an unbounded string would still become an unbounded query parameter,
	// and a cursor is always a UUID.
	if id == "" || len(id) > 64 {
		return nil, errBadCursor
	}
	return &Cursor{OccurredAt: time.Unix(0, n).UTC(), ID: id}, nil
}

// ─── Retention ──────────────────────────────────────────────────────────────

// Purge deletes events older than the retention window and reports how many.
//
// The caller decides WHEN. See RunRetention in the composition root for the
// schedule and the reasoning behind it.
func (s *Service) Purge(ctx context.Context, retention time.Duration) (int64, error) {
	// A non-positive window would mean "delete everything", which no
	// configuration should be able to express by accident. Config validation
	// rejects it too; this is the second line, in the function that does the
	// deleting.
	if retention <= 0 {
		return 0, nil
	}
	cutoff := s.now().UTC().Add(-retention)
	return s.store.DeleteOlderThan(ctx, cutoff)
}
