// Package audit defines the canonical audit-event model emitted by the
// admin/identity layer when an actor mutates a user, role, session, or
// invitation. The model is deliberately provider-agnostic — it knows
// nothing about Keycloak, gin, or the request lifecycle — so the same
// event shape can flow to logs today and to a database table in
// Sprint 4 (Observability Foundation) without breaking consumers.
//
// Required fields (per the v0.2 observability scope):
//
//	who       → Actor    (Subject / Email / Username)
//	action    → Action   (canonical verb, e.g. "user.created")
//	target    → Target   (Kind / ID / Name)
//	timestamp → Timestamp (UTC)
//	ip        → IP       (client IP captured at the request edge)
//
// Reason and Extra are optional and exist so failure paths and per-event
// nuance (e.g. "roles=[editor,support]") can ride along without
// destabilising the core fields.
package audit

import "time"

// Action is the canonical verb identifying a mutation. Values are stable
// over a major version — adding a new value is backwards-compatible,
// renaming or removing one is breaking for log/metric consumers.
type Action string

// User mutations.
const (
	ActionUserCreated       Action = "user.created"
	ActionUserUpdated       Action = "user.updated"
	ActionUserDeleted       Action = "user.deleted"
	ActionUserRolesGranted  Action = "user.roles_granted"
	ActionUserRoleRevoked   Action = "user.role_revoked"
	ActionUserPasswordReset Action = "user.password_reset"
)

// Role mutations.
const (
	ActionRoleCreated Action = "role.created"
	ActionRoleUpdated Action = "role.updated"
	ActionRoleDeleted Action = "role.deleted"
)

// Session revokes. UserSessionsLoggedOut covers the "log them out of
// everywhere" admin action; SessionRevoked is the single-session form.
const (
	ActionSessionRevoked        Action = "session.revoked"
	ActionUserSessionsLoggedOut Action = "user.sessions_logged_out"
)

// Invitation lifecycle.
const (
	ActionInvitationCreated Action = "invitation.created"
	ActionInvitationResent  Action = "invitation.resent"
	ActionInvitationRevoked Action = "invitation.revoked"
)

// Workspace lifecycle. The control plane's outermost boundary: a workspace is
// the unit every other object belongs to, and creating or archiving one changes
// which tenants exist.
//
// Added in Slice 10. These mutations previously emitted NOTHING — the durable
// trail's first finding was that the most consequential control-plane
// operations were the least recorded.
const (
	ActionWorkspaceCreated  Action = "workspace.created"
	ActionWorkspaceRenamed  Action = "workspace.renamed"
	ActionWorkspaceArchived Action = "workspace.archived"
)

// Connection lifecycle. The most security-sensitive events in the system.
//
// A connection holds a provider's ADMINISTRATIVE credential and decides which
// realm a whole workspace routes through. `connection.activated` in particular
// silently redirects every subsequent identity operation for that workspace to
// a different realm — a change no other event can be reconstructed from, and
// one that emitted nothing at all before Slice 10.
const (
	ActionConnectionCreated   Action = "connection.created"
	ActionConnectionUpdated   Action = "connection.updated"
	ActionConnectionVerified  Action = "connection.verified"
	ActionConnectionActivated Action = "connection.activated"
	ActionConnectionRetired   Action = "connection.retired"
	ActionConnectionDeleted   Action = "connection.deleted"
)

// Project and machine-credential lifecycle. Control-plane events: they change
// who may call this API, not what is in a realm.
const (
	ActionProjectCreated  Action = "project.created"
	ActionProjectRenamed  Action = "project.renamed"
	ActionProjectArchived Action = "project.archived"

	ActionCredentialCreated Action = "project_credential.created"
	ActionCredentialRevoked Action = "project_credential.revoked"
)

// ActorType discriminates the kind of principal that performed an action.
type ActorType string

const (
	// ActorOperator is a human using the console, authenticated by OIDC.
	ActorOperator ActorType = "operator"
	// ActorProject is a backend authenticated by a project credential.
	ActorProject ActorType = "project"
)

// Actor identifies WHO performed the action.
//
// It is a discriminated record, not a bag of optional strings. Two kinds of
// principal can now act on this API, and the fields are disjoint by kind:
//
//	operator → Subject, Email, Username     (Keycloak identity)
//	project  → ProjectID, CredentialID      (machine credential)
//
// A PROJECT ID MUST NEVER APPEAR IN Subject. Subject means "a Keycloak sub",
// and every consumer that has ever read this struct — log queries, the audit
// viewer, the self-protection guards' mental model — reads it that way. Putting
// a `prj_` value there would make a machine indistinguishable from a human in
// exactly the records that exist to tell them apart. ActorFromGin is the only
// constructor used in production and cannot produce that shape.
//
// The new fields are additive and `omitempty`, so an existing operator event
// serialises byte-identically apart from the `type` discriminator.
type Actor struct {
	// Type is "operator" or "project". Empty only for an unattributed event
	// (no principal on the request), which is preserved as a visible gap
	// rather than defaulted to a plausible value.
	Type ActorType `json:"type,omitempty"`

	// Operator fields.
	Subject  string `json:"subject,omitempty"`
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`

	// Project fields. ProjectID and CredentialID are the public `prj_` and
	// `key_` ids: the credential id is what an operator revokes, so an audit
	// line names the exact key to pull rather than the project that held it.
	ProjectID    string `json:"project_id,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`
}

// Target identifies WHAT was acted upon. Kind is a short string
// ("user", "role", "session", "invitation") that lets log consumers
// filter quickly; ID is the canonical identifier (Keycloak sub UUID,
// role name, session UUID); Name is an optional human label (email,
// display name) that costs nothing extra at the call site.
type Target struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Event is the canonical audit record. Construct one and hand it to
// audit.Record — the package stamps Timestamp if you leave it zero and
// dispatches to the currently-registered Recorder.
type Event struct {
	Action Action `json:"action"`
	Actor  Actor  `json:"actor"`
	Target Target `json:"target"`
	// Workspace is the public `ws_` id of the workspace whose provider the
	// mutation was routed through. Empty for legacy /admin/* operations,
	// which have no workspace — they act on the process-level realm.
	//
	// `omitempty` is what makes that distinction readable rather than
	// ambiguous: an event with no `workspace` key came from the unscoped
	// legacy surface, and an event with one names exactly which realm was
	// touched. Without it, "user.deleted" in a multi-workspace installation
	// says a user was deleted somewhere and offers no way to find out where.
	//
	// The public id, never the bare UUID: it is the identifier that appears
	// in the request path and in every /v1 response, so an operator reading
	// a log line can paste it straight back into an API call.
	Workspace string `json:"workspace,omitempty"`
	IP        string `json:"ip,omitempty"`

	// RequestID ties this event to the response the caller received and to the
	// server log lines for the same request. Present for /v1 operations, where
	// requestid.Middleware is mounted; empty for legacy /admin/*, which
	// deliberately has no correlation header.
	RequestID string `json:"request_id,omitempty"`

	// UserAgent is the caller's declared client, when it sent one. It is the
	// cheapest signal for telling one backend deployment from another when two
	// share a credential they should not have shared — and it is why the field
	// exists at all, since operators come through one console.
	//
	// Bounded at capture time: it is attacker-controlled input on its way to a
	// log.
	UserAgent string `json:"user_agent,omitempty"`

	Timestamp time.Time      `json:"ts"`
	Reason    string         `json:"reason,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`

	// PersistedInTransaction reports that this event's durable row was ALREADY
	// written, inside the same PostgreSQL transaction as the mutation it
	// describes.
	//
	// It exists so that control-plane mutations can be atomic with their audit
	// row ([TD-033]) without growing a second emission path. Every mutation
	// still ends with one audit.Record call, and every sink still sees the
	// event; the durable recorder is the only one that reads this field, and it
	// skips the row it already wrote. Without the flag the choice would be
	// between a duplicate row and a separate "emit to everything except the
	// database" API that call sites could pick wrongly.
	//
	// `json:"-"`: this is a fact about how the event was delivered, not about
	// what happened. A log line saying so would be describing the plumbing.
	//
	// It is set by the SERVICE that performed the transactional write, and by
	// nothing else. A call site that sets it without having written the row
	// would silently delete the event from the durable trail —
	// TestRecorder_OnlyATransactionalWriterMaySetTheMarker is the guard.
	//
	// [TD-033]: docs/TECH_DEBT.md#td-033
	PersistedInTransaction bool `json:"-"`
}
