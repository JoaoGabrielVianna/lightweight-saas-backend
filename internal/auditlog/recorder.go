package auditlog

import (
	"context"
	"fmt"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
)

var log = logger.New("auditlog")

// Recorder persists emitted events. It implements audit.Recorder, so it plugs
// into the existing fan-out beside the log sink and the ring buffer and no
// producer changes.
type Recorder struct {
	store Store
}

// NewRecorder wraps a Store. Returns nil for a nil store so the composition
// root omits durable audit rather than wiring a recorder that panics.
func NewRecorder(store Store) *Recorder {
	if store == nil {
		return nil
	}
	return &Recorder{store: store}
}

// Record maps an emitted event onto a durable row and writes it.
//
// ─── The failure policy, stated rather than emergent ────────────────────────
//
// A durable write can fail after the business mutation has already happened.
// There are three possible answers and this package picks one, on purpose:
//
//	A  succeed the response, log loudly, count it      ← chosen
//	B  fail the response
//	C  outbox and retry
//
// **B is actively dangerous for provider mutations.** A Keycloak user has been
// created; telling the caller it failed invites a retry, and the retry either
// creates a second user or answers 409 for a user the caller believes does not
// exist. The client ends up with a wrong model of the world because we could
// not write a log row. Losing an audit row is bad; corrupting the caller's
// understanding of what exists is worse.
//
// **C is out of scope**, and would be premature: an outbox is a table, a
// worker, a retry policy and a poison-message story, and it is only worth that
// once the failure has been observed rather than imagined.
//
// **A is chosen for both classes**, and the honesty is in what comes with it:
// an ERROR log carrying the request id, and `lightweight_audit_persist_failures_total`
// so the condition is alertable. A trail that is silently incomplete is worse
// than one that is loudly incomplete — the first invites trust it has not
// earned.
//
// ─── Why control-plane mutations are not transactional ──────────────────────
//
// Workspace, connection, project and credential mutations write to the SAME
// PostgreSQL as this table, so a single transaction is theoretically available
// and would make those events exactly complete.
//
// It is not done, and the reason is the shape of the change rather than the
// difficulty: the audit event is emitted by the HTTP handler AFTER the service
// returns, so atomicity would mean threading a transaction through
// service→repository for three domains and every one of their tests, and
// having each service construct the audit event it currently knows nothing
// about. That is a large refactor whose benefit is bounded by how often the two
// writes can diverge — and since they share a database and a pool, the common
// failure (database down) fails both.
//
// What is left is a narrow window: pool exhaustion between the two statements,
// or the process dying between them. That is recorded as [TD-033] with the
// transactional design described, rather than left as an unexamined property.
//
// [TD-033]: docs/TECH_DEBT.md#td-033
func (r *Recorder) Record(ctx context.Context, e audit.Event) {
	// Already written, inside the mutation's own transaction. Writing it again
	// here would produce two rows for one event — and the second one would be
	// outside the transaction, which is the exact property the first one exists
	// to have. See audit.Event.PersistedInTransaction.
	//
	// The other sinks in the fan-out (the log line, the in-process ring) do NOT
	// check this flag, because they are not what was written transactionally.
	// One emission, every sink, no duplicate row.
	if e.PersistedInTransaction {
		return
	}

	rec, ok := r.toRecord(e)
	if !ok {
		return
	}

	if err := r.store.Record(ctx, rec); err != nil {
		metrics.Default.ObserveAuditPersistFailure(rec.EventType)

		// Everything in this line is safe to log and is what an operator needs
		// to reconstruct what was lost: which event, for which workspace, on
		// which request. Never the metadata, never the reason text, never the
		// actor's email.
		log.Error("DURABLE AUDIT WRITE FAILED — the mutation succeeded and its" +
			" history was not recorded" +
			" event=" + rec.EventType +
			" outcome=" + string(rec.Outcome) +
			" workspace_id=" + orNone(rec.WorkspaceID) +
			" request_id=" + orNone(rec.RequestID) +
			": " + err.Error())
	}
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// toRecord maps an emitted event onto a row, dropping the event when it cannot
// be attributed.
//
// ok=false means "do not persist". Two cases, and both are deliberate:
//
//	no actor          an event nobody performed cannot answer "who". The log
//	                  sink still has it; the trail does not pretend.
//	unparseable
//	workspace         a workspace-scoped event whose workspace cannot be
//	                  resolved would be written with NULL and become invisible
//	                  to the only API that reads it — a row that exists and
//	                  cannot be found is worse than a logged failure.
func (r *Recorder) toRecord(e audit.Event) (Record, bool) {
	id, err := publicid.New()
	if err != nil {
		log.Error("cannot generate an audit event id: " + err.Error())
		return Record{}, false
	}

	rec := Record{
		ID:         id,
		EventType:  string(e.Action),
		Outcome:    outcomeOf(e),
		RequestID:  e.RequestID,
		SourceIP:   validIP(e.IP),
		OccurredAt: e.Timestamp.UTC(),
	}

	// ── Workspace ───────────────────────────────────────────────────────────
	//
	// audit.Event carries the PUBLIC id (ws_…) because that is what belongs in
	// a log line; the column is a UUID foreign key. Parse accepts both the
	// prefixed and bare forms, which matters because the project handlers pass
	// the raw path parameter.
	if e.Workspace != "" {
		uuid, err := publicid.Parse(publicid.WorkspacePrefix, e.Workspace)
		if err != nil {
			log.Error("audit event names an unusable workspace id; not persisted" +
				" event=" + rec.EventType +
				" request_id=" + orNone(rec.RequestID))
			return Record{}, false
		}
		rec.WorkspaceID = uuid
	}

	// ── Actor ───────────────────────────────────────────────────────────────
	//
	// Disjoint by type, matching the database CHECK. A project NEVER occupies
	// ActorSubject: that field means "a Keycloak sub", and a prj_ value there
	// would make a machine indistinguishable from a person in exactly the
	// records that exist to tell them apart.
	switch e.Actor.Type {
	case audit.ActorOperator:
		rec.ActorType = audit.ActorOperator
		rec.ActorSubject = e.Actor.Subject
		rec.ActorEmail = e.Actor.Email
	case audit.ActorProject:
		rec.ActorType = audit.ActorProject
		rec.ActorProjectID = e.Actor.ProjectID
		rec.ActorCredentialID = e.Actor.CredentialID
		if rec.ActorProjectID == "" {
			log.Error("project audit event carries no project id; not persisted" +
				" event=" + rec.EventType)
			return Record{}, false
		}
	default:
		// An unattributed event. The ring and the log keep it; the durable
		// trail does not, because the whole question it answers is "who".
		return Record{}, false
	}

	// ── Resource ────────────────────────────────────────────────────────────
	//
	// An unrecognised kind is dropped rather than allowed to fail the INSERT on
	// the CHECK. Losing the resource label costs a column; losing the row costs
	// the actor, the verb and the outcome, which are the parts that matter.
	if kind := ResourceType(e.Target.Kind); kind != "" {
		if IsKnownResourceType(kind) {
			rec.ResourceType = kind
			rec.ResourceID = e.Target.ID
		} else {
			log.Warn("audit event names an unknown resource kind " + string(kind) +
				"; recording the event without it (event=" + rec.EventType + ")")
			rec.ResourceID = e.Target.ID
		}
	}

	// ── Failure reason ──────────────────────────────────────────────────────
	//
	// audit.Event.Reason is `err.Error()` — free text that can contain a
	// Keycloak response body, a SQL fragment or a customer's email. It NEVER
	// reaches the table. What is stored is a code from the closed /v1
	// vocabulary, derived from the text by an exact-match lookup; anything
	// unrecognised becomes the generic marker rather than the original string.
	if rec.Outcome == OutcomeFailure {
		rec.ReasonCode = reasonCodeFor(e.Reason)
	}

	// ── Metadata ────────────────────────────────────────────────────────────
	rec.Metadata = allowlistMetadata(rec.EventType, e.Extra)

	// Target.Name is deliberately NOT persisted.
	//
	// It is the one free-text field a call site sets from the request — an
	// email on user.created, a label on credential.created — and this table is
	// readable by anyone holding audit:read. ResourceID identifies the thing;
	// the name is display sugar, and display sugar sourced from input is how a
	// password ends up in an audit row when someone mis-wires a call site.

	return rec, true
}

// outcomeOf derives success or failure.
//
// audit.Event has no outcome field: the emitters set Reason when, and only
// when, the operation failed. That is the existing contract
// (logging.recordMutation sets Reason from err), so deriving from it here keeps
// one source of truth rather than adding a field every call site could forget.
func outcomeOf(e audit.Event) Outcome {
	if e.Reason != "" {
		return OutcomeFailure
	}
	return OutcomeSuccess
}

// actorType maps a stored discriminant back, refusing anything unknown.
//
// The database CHECK makes an unknown value unreachable, so this only fires
// against a table someone edited by hand. Returning the zero value rather than
// guessing means such a row renders with no actor type instead of being
// silently reclassified as an operator.
func actorType(s string) audit.ActorType {
	switch audit.ActorType(s) {
	case audit.ActorOperator:
		return audit.ActorOperator
	case audit.ActorProject:
		return audit.ActorProject
	default:
		log.Warn(errUnknownActorType.Error() + ": " + s)
		return ""
	}
}

// RecordTx writes the durable row inside the caller's transaction and REPORTS
// the failure instead of absorbing it.
//
// ─── Why this exists alongside Record, which swallows ───────────────────────
//
// Record's policy — succeed the response, log loudly, count it — is correct for
// PROVIDER mutations and stays. A Keycloak user has been created; failing the
// response invites a retry that creates a second one, so the caller ends up
// with a wrong model of the world because we could not write a log row.
//
// Control-plane mutations are the opposite case, and that is what makes a
// second method honest rather than redundant. The domain row and this row live
// in the same PostgreSQL, so "the audit write failed" and "the mutation did not
// happen" can be made the same fact. Returning the error is what lets the
// caller's transaction roll back, and rolling back is what makes the failure
// safe to retry.
//
// The returned error always wraps audit.ErrNotRecorded, so a caller can tell it
// from a domain failure without inspecting strings. That distinction has one
// job: the handler must NOT respond to it by recording a failure event, because
// that would be a second write to the store that just failed.
//
// ─── What "not persistable" means here ──────────────────────────────────────
//
// toRecord drops events it cannot attribute — no actor, an unparseable
// workspace. Through Record that is a silent skip, which is right for a
// best-effort trail. Through this path it is an ERROR, because a control-plane
// mutation classified as requiring durable audit that produced no writable row
// has not met its requirement, and committing it would be exactly the divergence
// this method exists to prevent.
func (r *Recorder) RecordTx(ctx context.Context, tx database.Tx, e audit.Event) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}

	rec, ok := r.toRecord(e)
	if !ok {
		metrics.Default.ObserveAuditPersistFailure(string(e.Action))
		return fmt.Errorf("%w: event %s cannot be attributed to an actor and a workspace",
			audit.ErrNotRecorded, e.Action)
	}

	if err := r.store.WithTx(tx).Record(ctx, rec); err != nil {
		metrics.Default.ObserveAuditPersistFailure(rec.EventType)

		// Logged here rather than only at the call site, because this is the
		// layer that knows WHICH event failed. Everything on the line is safe:
		// never the metadata, never the reason text, never an actor's email.
		//
		// Deliberately NOT recorded as an audit event of its own. The store
		// that would receive it is the one that just failed, and a recursive
		// write is how a transient failure becomes an outage.
		log.Error("DURABLE AUDIT WRITE FAILED inside a control-plane transaction —" +
			" the mutation is being rolled back" +
			" event=" + rec.EventType +
			" workspace_id=" + orNone(rec.WorkspaceID) +
			" request_id=" + orNone(rec.RequestID) +
			": " + err.Error())

		return fmt.Errorf("%w: %s", audit.ErrNotRecorded, rec.EventType)
	}
	return nil
}
