package lightweight

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// AuditService reads the workspace's durable audit trail.
//
// The trail records attempts to CHANGE STATE by an identified actor — who did
// what, to which resource, whether it worked, and on which request. Reads do not
// appear in it, including these ones.
//
// It is workspace-wide, not project-wide: a credential holding audit:read can
// see what operators and what OTHER projects did in this workspace. That is a
// real capability, which is why audit:read is its own scope rather than part of
// users:read.
//
// Obtain it from [Client.Audit].
type AuditService struct {
	client *Client
}

// AuditListOptions filters and pages [AuditService.List].
//
// Every field is optional. Filters combine with AND.
type AuditListOptions struct {
	// Event matches one exact event type, e.g. "user.created". There is no
	// prefix or wildcard matching.
	Event string

	// ActorType restricts to operators or to machines.
	ActorType AuditActorType

	// Outcome restricts to successes or to failures. Filtering to
	// [AuditOutcomeFailure] is the cheapest way to find refused attempts.
	Outcome AuditOutcome

	// From and To bound OccurredAt inclusively. A zero time means unbounded on
	// that side. They are sent as RFC 3339 in UTC.
	From time.Time
	To   time.Time

	// Cursor resumes from a previous page's [AuditPagination.NextCursor]. Empty
	// starts at the newest event.
	//
	// The value is opaque: it is not a timestamp, not an offset, and must not be
	// constructed or modified. Pass back exactly what the server returned.
	Cursor string

	// Limit is the page size, 1-200. Zero means the server's default of 50. The
	// server clamps; [AuditPagination.Limit] reports what was applied.
	Limit int
}

func (o AuditListOptions) query() url.Values {
	q := url.Values{}
	if o.Event != "" {
		q.Set("event", o.Event)
	}
	if o.ActorType != "" {
		q.Set("actor_type", string(o.ActorType))
	}
	if o.Outcome != "" {
		q.Set("outcome", string(o.Outcome))
	}
	if !o.From.IsZero() {
		q.Set("from", o.From.UTC().Format(time.RFC3339))
	}
	if !o.To.IsZero() {
		q.Set("to", o.To.UTC().Format(time.RFC3339))
	}
	if o.Cursor != "" {
		q.Set("cursor", o.Cursor)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	return q
}

// List returns one page of audit events, newest first.
//
// Pagination is by cursor, and the SERVER's model is exposed rather than hidden:
// pass [AuditPagination.NextCursor] back as [AuditListOptions.Cursor], and stop
// when it is empty. Use [AuditPage.HasMore] rather than inspecting len(Items) —
// a full page can be the last one.
//
//	opts := lightweight.AuditListOptions{Outcome: lightweight.AuditOutcomeFailure}
//	for {
//		page, err := client.Audit.List(ctx, opts)
//		if err != nil {
//			return err
//		}
//		for _, ev := range page.Items {
//			handle(ev)
//		}
//		if !page.HasMore() {
//			break
//		}
//		opts.Cursor = page.Pagination.NextCursor
//	}
//
// Required scope: audit:read.
func (s *AuditService) List(ctx context.Context, opts AuditListOptions) (*AuditPage, error) {
	const op = "Audit.List"
	path := s.client.workspacePath("audit")

	var out AuditPage
	if err := s.client.do(ctx, op, http.MethodGet, path, opts.query(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// All walks every matching event across pages.
//
// It is a convenience over [AuditService.List], not a replacement for it: the
// page API stays the primary one because a caller that needs to checkpoint,
// resume after a restart, or stop after N pages needs the cursor in its own
// hands. Reach for All when the whole result set is genuinely wanted and is
// known to be bounded.
//
//	for ev, err := range client.Audit.All(ctx, opts) {
//		if err != nil {
//			return err
//		}
//		handle(ev)
//	}
//
// # How it ends
//
// Iteration stops when the server reports no further pages, when the loop body
// breaks, or when a request fails. A failure yields ONE final pair with a
// non-nil error and a zero event, then stops — so a body that ignores the error
// terminates rather than looping. Cancel ctx to stop it from outside; that
// surfaces as a [RequestError] wrapping context.Canceled.
//
// [AuditListOptions.Cursor] is honoured as the starting position and is not
// modified: opts is taken by value.
//
// Required scope: audit:read.
func (s *AuditService) All(ctx context.Context, opts AuditListOptions) iter.Seq2[AuditEvent, error] {
	return func(yield func(AuditEvent, error) bool) {
		for {
			page, err := s.List(ctx, opts)
			if err != nil {
				yield(AuditEvent{}, err)
				return
			}
			for _, ev := range page.Items {
				if !yield(ev, nil) {
					return
				}
			}
			if !page.HasMore() {
				return
			}
			// The server's cursor, verbatim. Deriving a position from the last
			// item's timestamp would be a second, subtly different pagination
			// implementation living in the client.
			opts.Cursor = page.Pagination.NextCursor
		}
	}
}
