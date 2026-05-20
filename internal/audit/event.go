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

// Actor identifies WHO performed the action. At least one of Subject /
// Email / Username should be populated; consumers display the most
// human-readable one available.
type Actor struct {
	Subject  string `json:"subject,omitempty"`
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
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
	Action    Action         `json:"action"`
	Actor     Actor          `json:"actor"`
	Target    Target         `json:"target"`
	IP        string         `json:"ip,omitempty"`
	Timestamp time.Time      `json:"ts"`
	Reason    string         `json:"reason,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}
