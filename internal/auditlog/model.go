// Package auditlog is the DURABLE audit trail: the store that outlives the
// process, and the workspace-scoped API that reads it.
//
// # Two packages, one subject, and why
//
//	internal/audit     the event MODEL and the emission seam. Imported by every
//	                   producer. Knows nothing about storage, gin or SQL.
//	internal/auditlog  PERSISTENCE and QUERY. Imported by the composition root
//	                   and by nothing that emits.
//
// Keeping them apart is what stops a handler that wants to record something
// from acquiring a database dependency. A producer calls
// `logging.RecordWorkspaceMutation` and does not know, and must not know,
// whether the event ends up in a log line, a ring buffer or a table.
//
// # What this is
//
// A record of ATTEMPTS TO CHANGE STATE by an identified actor. Not a request
// log. Reads are absent, health checks are absent, and so is anything that
// never got past authentication — an anonymous flood produces zero rows, which
// is what keeps the table bounded by real activity rather than by traffic.
//
// # What it replaces, and what it does not
//
// It does not replace `audit.MemoryRecorder`. The ring still backs
// `/admin/audit-events`, which is process-level, operator-only, and must stay
// byte-compatible. The two have disjoint surfaces and therefore disjoint
// authority: the ring answers "what just happened on this box", the table
// answers "what has ever happened in this workspace". Making one serve both
// would mean either giving the legacy endpoint a workspace it does not have, or
// giving the new one a 500-event horizon.
package auditlog

import (
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
)

// EventPrefix renders audit event ids as evt_<uuid>.
//
// Declared here rather than in internal/publicid because that package's
// prefixes are for domain entities a caller addresses by id, and an audit event
// is never addressed — there is no GET /audit/{id}. It has a public id so a
// support conversation can name one row, and so a cursor can be stable.
const EventPrefix = "evt"

// Outcome is whether the attempt succeeded.
//
// Two values, closed. A third ("partial", "unknown") would be a modelling
// failure: an attempt either changed state or did not, and an event we cannot
// classify is one we should not be writing.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// ResourceType is the kind of thing acted upon.
//
// A closed set, mirrored by a CHECK constraint in migration 000006. Closed
// because the only operations on it are equality and display: there is nothing
// to join to — a Keycloak user id is not a row in this database — so a
// polymorphic resource table would be a join to nowhere.
type ResourceType string

const (
	ResourceWorkspace  ResourceType = "workspace"
	ResourceConnection ResourceType = "connection"
	ResourceProject    ResourceType = "project"
	ResourceCredential ResourceType = "project_credential"
	ResourceUser       ResourceType = "user"
	ResourceRole       ResourceType = "role"
	ResourceSession    ResourceType = "session"
	ResourceInvitation ResourceType = "invitation"
)

// AllResourceTypes is the vocabulary, and MUST agree with the
// audit_events_resource_type_check constraint in migration 000006.
// TestResourceTypes_MatchTheDatabaseConstraint pins that.
func AllResourceTypes() []ResourceType {
	return []ResourceType{
		ResourceWorkspace, ResourceConnection, ResourceProject, ResourceCredential,
		ResourceUser, ResourceRole, ResourceSession, ResourceInvitation,
	}
}

// IsKnownResourceType reports whether t is in the vocabulary.
//
// Used by the recorder to drop an unrecognised kind rather than let the INSERT
// fail on the CHECK: a mistyped resource kind must not be able to lose the
// whole event, because the actor, the verb and the outcome are the parts that
// matter and they are still correct.
func IsKnownResourceType(t ResourceType) bool {
	for _, known := range AllResourceTypes() {
		if known == t {
			return true
		}
	}
	return false
}

// Record is one durable row.
//
// It is deliberately NOT audit.Event. That type is the emission contract shared
// by every producer and shaped for a log line; this one is shaped for a table
// and for the read API, and the difference is not cosmetic:
//
//	audit.Event.Workspace  is a PUBLIC id (ws_…), because it appears in logs
//	Record.WorkspaceID     is a UUID, because it is a foreign key
//
//	audit.Event.Reason     is free text from an error, which must never persist
//	Record.ReasonCode      is a code from the /v1 vocabulary
//
// Mapping between them is one function (recorder.go), which is the single place
// those conversions — and the redaction they imply — can be reviewed.
type Record struct {
	// ID is the internal UUID. PublicID renders it as evt_<uuid>.
	ID string

	// WorkspaceID is the workspace's UUID, or empty for a global event.
	//
	// Empty means "not workspace-scoped", which today is only the legacy
	// /admin/* surface. The workspace API filters on equality, so an empty one
	// is unreachable through it by construction.
	WorkspaceID string

	EventType string
	Outcome   Outcome

	ActorType    audit.ActorType
	ActorSubject string
	ActorEmail   string
	// ActorProjectID and ActorCredentialID are public ids (prj_…, key_…),
	// stored as text and NOT as foreign keys: history must outlive the
	// credential it describes, and a revoked credential's actions are exactly
	// what an investigation is looking for.
	ActorProjectID    string
	ActorCredentialID string

	ResourceType ResourceType
	ResourceID   string

	RequestID string

	// SourceIP is the address the server observed, validated to parse as an IP
	// before it is written.
	//
	// There is deliberately no UserAgent. It is free text the caller controls,
	// and this table is read by anyone holding audit:read — see the note in
	// migration 000006.
	SourceIP string

	ReasonCode string
	Metadata   map[string]any

	OccurredAt time.Time
}

// Cursor is a position in the descending (occurred_at, id) ordering.
//
// A composite, because occurred_at alone is not unique: twenty mutations in one
// request burst can share a millisecond, and a cursor on the timestamp alone
// would either skip them or repeat them at a page boundary. The id breaks the
// tie deterministically, which is what makes "no duplicate, no skip" a property
// rather than a hope.
type Cursor struct {
	OccurredAt time.Time
	ID         string
}

// Query is a page request. Every field narrows; none can widen.
//
// WorkspaceID is set by the handler from the AUTHORIZED path parameter and is
// never readable from the query string. That is the workspace boundary: there
// is no way to express "all workspaces" in this type, so no filter can escape
// one.
type Query struct {
	// WorkspaceID is the workspace's UUID. Required — a zero value returns
	// nothing rather than everything, enforced in the repository.
	WorkspaceID string

	EventType string
	ActorType audit.ActorType
	Outcome   Outcome
	From      time.Time
	To        time.Time

	// After is the exclusive upper bound in the descending ordering: results
	// are strictly older than this position.
	After *Cursor

	// Limit is the page size, already clamped by the service.
	Limit int
}

// Page is one page of results plus the position to resume from.
type Page struct {
	Items []Record

	// NextCursor is nil when this is the last page.
	//
	// Derived from whether the repository found MORE rows than the page size,
	// not from `len(Items) == Limit`. The two differ on the exact boundary,
	// where the second produces a final page that promises another and then
	// returns nothing — a client that stops on an empty page still works, and
	// one that shows "load more" does not.
	NextCursor *Cursor
}
