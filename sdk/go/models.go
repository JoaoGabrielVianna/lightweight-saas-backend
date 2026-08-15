package lightweight

import "time"

// The public wire models.
//
// They are RE-DECLARED here rather than shared with the server, and the
// duplication is the design. The HTTP contract is the boundary between the two,
// so a field the server adds appears here only when someone decides it should —
// which is what makes an accidental change to the server's internal types
// unable to change this package's public API. It also means this directory can
// be lifted into its own repository without carrying anything with it.
//
// Names are chosen for a reader of Go, not for a reader of the transport:
// [UserPage] rather than ListUsersResponse. What is NOT renamed is any actual
// concept — a Role is a Role.

// User is an identity in the workspace.
type User struct {
	// ID is the provider's stable identifier, a UUID. It is what every other
	// user-scoped method takes.
	ID string `json:"id"`

	// Username is the login name. This API creates users with the username set
	// to the email address.
	Username string `json:"username"`

	// Email is the user's address.
	Email string `json:"email"`

	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`

	// Enabled reports whether the user may authenticate. A disabled user still
	// exists and keeps their roles.
	Enabled bool `json:"enabled"`

	// EmailVerified reports whether the address has been confirmed.
	EmailVerified bool `json:"email_verified"`

	// CreatedAt is when the user record was created, in UTC.
	CreatedAt time.Time `json:"created_at"`

	// Attributes are provider-side custom attributes. Absent for most users;
	// multi-valued because the underlying store is.
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// UserPage is one page of users.
//
// Users is the ONLY collection in this API with offset pagination, and this type
// deliberately does not look like the others: pretending everything paginates
// identically would let a caller write one loop that is wrong for two of the
// three models. See [UserListOptions].
type UserPage struct {
	// Users are the users on this page.
	Users []User `json:"users"`

	// First is the offset that produced this page, echoed by the server.
	First int `json:"first"`

	// Max is the page size that was applied, echoed by the server. It may be
	// smaller than the one requested: the server clamps it.
	Max int `json:"max"`

	// Count is the number of users IN THIS PAGE — always len(Users), never a
	// total. There is no total: obtaining one would mean counting the whole
	// directory on every request.
	Count int `json:"count"`
}

// Role is a role that can be granted to users in the workspace.
type Role struct {
	// ID is the provider's identifier. Roles are addressed by [Role.Name]
	// everywhere in this API; the id is informational.
	ID string `json:"id"`

	// Name is the role's natural key and what every role method takes.
	Name string `json:"name"`

	// Description is free prose, and the only field an update may change.
	Description string `json:"description"`

	// Composite reports whether this role confers other roles.
	Composite bool `json:"composite"`

	// Builtin reports whether the role is part of the platform rather than
	// created by an operator. Builtin roles are protected: attempts to modify or
	// delete them are refused with [CodeRoleReserved], and a Project Credential
	// cannot grant or revoke the administrative ones at all
	// ([CodeRolePrivileged]).
	Builtin bool `json:"builtin"`
}

// Session is one active authenticated session.
type Session struct {
	// ID identifies the session and is what [SessionsService.Revoke] takes.
	ID string `json:"id"`

	// UserID is the user the session belongs to.
	UserID string `json:"user_id"`

	// Username is that user's login name, included so a listing is readable
	// without a second call per row.
	Username string `json:"username"`

	// IPAddress is where the session was established from.
	IPAddress string `json:"ip_address"`

	// UserAgent is the client that established it, when known.
	UserAgent string `json:"user_agent,omitempty"`

	// StartedAt is when the session began, in UTC.
	StartedAt time.Time `json:"started_at"`

	// LastAccess is the most recent activity on the session, in UTC.
	LastAccess time.Time `json:"last_access"`

	// Clients maps the applications this session has been used against.
	Clients map[string]string `json:"clients,omitempty"`
}

// Invitation is a user who has been invited but has not yet completed sign-up.
//
// An invitation IS a user record in an incomplete state, which is why revoking
// one deletes that user. See [InvitationsService.Revoke].
type Invitation struct {
	// ID is the invited user's identifier — the same identifier
	// [UsersService.Get] takes once they have accepted.
	ID string `json:"id"`

	// Email is where the invitation was sent.
	Email string `json:"email"`

	// Username is the login name the account will have.
	Username string `json:"username"`

	// RequiredActions are the steps the user must complete before the account
	// becomes usable, e.g. setting a password.
	RequiredActions []string `json:"required_actions"`

	// InvitedBy records who sent it, when the sender supplied a value.
	InvitedBy string `json:"invited_by,omitempty"`

	// ExpiresAt is the invitation's expiry, as a string.
	//
	// It is NOT a time.Time, and that is a truthfulness decision rather than an
	// oversight. This API writes the value in RFC 3339 and normalises it on
	// creation, but it reads the value back out of a provider-side attribute
	// that other tooling can also write. Typing it as a time.Time would mean
	// this package either silently discarded a value it could not parse or
	// failed to decode a response that was otherwise fine. Use
	// [Invitation.ExpiresAtTime] to get the parsed value together with whether
	// parsing succeeded.
	ExpiresAt string `json:"expires_at,omitempty"`

	// CreatedAt is when the invitation was created, in UTC.
	CreatedAt time.Time `json:"created_at"`

	// Status is the invitation's state as the server reports it, e.g. "pending".
	Status string `json:"status"`
}

// ExpiresAtTime parses [Invitation.ExpiresAt].
//
// ok is false when the invitation has no expiry, or when the stored value is not
// RFC 3339 — which can happen only if something other than this API wrote the
// attribute. Callers that treat "no expiry" and "unparseable expiry" the same
// way can ignore the distinction; callers that alert on malformed data have the
// information to do so.
func (i Invitation) ExpiresAtTime() (t time.Time, ok bool) {
	if i.ExpiresAt == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, i.ExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// AuditOutcome is whether an audited attempt succeeded.
//
// Typed because the vocabulary is genuinely closed and small, and because it is
// the one audit field a caller routinely filters on — a typo in a bare string
// there produces an empty result set rather than an error. Comparison against an
// unrecognised value still works: this is a string type, not a validated enum,
// so a server that grew a third outcome would still decode.
type AuditOutcome string

const (
	// AuditOutcomeSuccess — the attempt changed state as requested.
	AuditOutcomeSuccess AuditOutcome = "success"
	// AuditOutcomeFailure — the attempt was refused or failed.
	AuditOutcomeFailure AuditOutcome = "failure"
)

// AuditActorType is the kind of principal that acted.
type AuditActorType string

const (
	// AuditActorOperator — a human operating the console.
	AuditActorOperator AuditActorType = "operator"
	// AuditActorProject — a machine using a Project Credential, as this package
	// does.
	AuditActorProject AuditActorType = "project"
)

// AuditActor is who performed an audited operation.
//
// The two principal kinds are disjoint: an operator carries Subject and Email
// and never a project id; a project carries ProjectID and CredentialID and never
// a subject. The shape of the value therefore tells you which kind it was, and
// [AuditActor.Type] says so explicitly.
type AuditActor struct {
	// Type is "operator" or "project".
	Type AuditActorType `json:"type"`

	// Subject identifies a human operator. Empty for a project.
	Subject string `json:"subject,omitempty"`
	// Email is that operator's address. Empty for a project.
	Email string `json:"email,omitempty"`

	// ProjectID identifies the project a machine actor belongs to. Empty for an
	// operator.
	ProjectID string `json:"project_id,omitempty"`
	// CredentialID identifies which of that project's credentials was used.
	// Empty for an operator.
	//
	// This is the field that makes revocation actionable: it names exactly the
	// key to revoke, without revoking a project's other integrations.
	CredentialID string `json:"credential_id,omitempty"`
}

// AuditResource is what an audited operation acted upon. Absent for events that
// act on nothing in particular.
type AuditResource struct {
	// Type is the kind of resource, e.g. "user".
	Type string `json:"type"`
	// ID is that resource's identifier.
	ID string `json:"id"`
}

// AuditEvent is one entry in the workspace's durable audit trail.
//
// The trail records ATTEMPTS TO CHANGE STATE by an identified actor. It is not a
// request log: reads do not appear, and neither does traffic that never
// authenticated.
//
// It never contains secret material — no password, no credential, no provider
// token, and no request body — whoever is reading it.
type AuditEvent struct {
	// ID identifies the event.
	ID string `json:"id"`

	// Event is the event type, e.g. "user.created". Stable and safe to branch
	// on; open, so a newer server may emit types this package has never seen.
	Event string `json:"event"`

	// Outcome is "success" or "failure".
	Outcome AuditOutcome `json:"outcome"`

	// Actor is who acted.
	Actor AuditActor `json:"actor"`

	// Resource is what was acted upon, when the event names one.
	Resource *AuditResource `json:"resource,omitempty"`

	// ReasonCode is present only on a failure, and is drawn from the same
	// vocabulary as [APIError.Code]. The full cause is in the server log line
	// for [AuditEvent.RequestID], never here.
	ReasonCode string `json:"reason_code,omitempty"`

	// Metadata is per-event detail. Allowlisted per event type server-side, so
	// its keys are bounded and it never carries user-supplied content
	// wholesale.
	Metadata map[string]any `json:"metadata,omitempty"`

	// RequestID correlates the event with the request that caused it — including
	// requests made by this package, whose errors carry the same value in
	// [APIError.RequestID].
	RequestID string `json:"request_id,omitempty"`

	// OccurredAt is when the attempt happened, in UTC.
	OccurredAt time.Time `json:"occurred_at"`
}

// AuditPage is one page of the audit trail.
//
// Cursor-paginated, unlike [UserPage], because the trail only grows at the head:
// an offset would shift under a caller between pages and silently skip or
// duplicate events.
type AuditPage struct {
	// Items are the events on this page, newest first.
	Items []AuditEvent `json:"items"`

	// Pagination carries the position to resume from.
	Pagination AuditPagination `json:"pagination"`
}

// AuditPagination is the cursor state for [AuditPage].
type AuditPagination struct {
	// Count is the number of events on this page — always len(Items), never a
	// total.
	Count int `json:"count"`

	// Limit is the page size the server applied, which may be smaller than the
	// one requested.
	Limit int `json:"limit"`

	// NextCursor is the opaque position to pass as [AuditListOptions.Cursor] to
	// obtain the next page.
	//
	// Its ABSENCE is the end-of-history signal, not an empty page. A correct
	// loop continues while it is non-empty and therefore makes exactly as many
	// requests as there are pages. Use [AuditPage.HasMore] rather than checking
	// len(Items).
	NextCursor string `json:"next_cursor,omitempty"`
}

// HasMore reports whether another page exists.
//
// Prefer it to len(Items) > 0: a full page can still be the last one, and an
// empty page is not the terminator.
func (p AuditPage) HasMore() bool { return p.Pagination.NextCursor != "" }
