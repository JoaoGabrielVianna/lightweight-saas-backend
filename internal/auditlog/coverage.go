package auditlog

import (
	"sort"
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
)

// The audit coverage registry.
//
// Every MUTATING /v1 route declares, here, either the event it emits or an
// explicit reason it emits none. A route with neither fails a test.
//
// # Why this exists as a table rather than as review
//
// The authorization registry established the pattern and the reason is the
// same: a property that has to hold for every route, checked by reading the
// diff, holds until the week someone is busy. Slice 10's own Phase 0 is the
// evidence — eleven control-plane mutations, including
// `connection.activated`, which silently redirects an entire workspace to a
// different realm, had been shipping with no audit event at all. Nobody decided
// that; it just never came up.
//
// So the decision is now mandatory and mechanical. A new mutating route added
// in a future slice cannot merge without someone writing down what it records,
// and "nothing" is a legal answer that has to be justified in the same line.
//
// # Why keyed on the route and not the handler
//
// Same reason authz is: the route is what a test can enumerate from the router
// gin actually built. A registry of handler names would be satisfied by
// renaming a function.

// coverage is what a route does about audit.
type coverage struct {
	// Event is the audit action the route emits on the happy path. Empty when
	// NotAuditedBecause is set.
	Event audit.Action

	// NotAuditedBecause is why a mutating route records nothing. Non-empty only
	// when Event is empty.
	NotAuditedBecause string

	// ControlPlane reports whether this mutation's authoritative state lives in
	// THIS PostgreSQL.
	//
	// Added in Slice 15, and it is the field the whole slice turns on. It is not
	// a label for how important the route is; it is the answer to "can the
	// mutation and its audit row be one transaction", and that answer is a fact
	// about where the state lives rather than a level of effort:
	//
	//	true   workspace, connection, project, credential — rows in this
	//	       database. The service commits the domain write and the audit row
	//	       together; a failed audit write ROLLS THE MUTATION BACK and the
	//	       caller gets a 500. See [TD-033].
	//
	//	false  users, roles, sessions, invitations — state in a Keycloak realm.
	//	       No PostgreSQL transaction can undo a provider write, so the audit
	//	       row is best-effort: attempted after the fact, logged and counted
	//	       on failure, and the response still succeeds. Failing the response
	//	       there would invite a retry that creates a SECOND user, which
	//	       corrupts the caller's model of the world to save a log row. The
	//	       residual window is [TD-038].
	//
	// TestCoverage_DurabilityMatchesWhereTheStateLives enforces the implication
	// in both directions — including the dangerous one, a provider mutation
	// claiming a guarantee no transaction can deliver.
	//
	// [TD-033]: docs/TECH_DEBT.md#td-033
	// [TD-038]: docs/TECH_DEBT.md#td-038
	ControlPlane bool
}

// audited declares a PROVIDER mutation: the event is emitted, best-effort.
func audited(a audit.Action) coverage { return coverage{Event: a} }

// atomic declares a CONTROL-PLANE mutation: the event is written inside the
// same PostgreSQL transaction as the domain rows, and a failure to write it
// rolls the mutation back.
//
// A separate constructor rather than a bool argument on `audited`, for the same
// reason `notAudited` does not exist: the two are different promises to the
// caller, and which one a route makes should be visible in the table without
// counting positional arguments.
func atomic(a audit.Action) coverage {
	return coverage{Event: a, ControlPlane: true}
}

// There is deliberately no `notAudited(reason)` constructor.
//
// EVERY mutating route in this registry is audited today, so the helper would
// be dead code — and a dead constructor is an invitation: the easy way out of a
// failing completeness gate should not be one keystroke away.
//
// The escape hatch still exists, as the NotAuditedBecause field, written as a
// struct literal:
//
//	routeKey("POST", "/v1/…"): {NotAuditedBecause: "why this records nothing"},
//
// Spelling it out is the point. Someone taking that route has to type the
// reason into a field named for the fact that they are opting out, and
// TestCoverage_EveryEntryIsOneThingOrTheOther refuses an entry that tries to be
// both.

func routeKey(method, path string) string { return method + " " + path }

const (
	v1Workspaces = "/v1/workspaces"
	v1Workspace  = "/v1/workspaces/:workspace_id"
	v1Project    = v1Workspace + "/projects/:project_id"
)

// registry is the complete audit classification for every mutating /v1 route.
//
// Read it as the specification it is: each line says what this system will be
// able to tell you afterwards about that operation.
//
// Routes emitting SEVERAL events are listed by their primary one — the
// invitation endpoints emit both `invitation.created` and `user.created`,
// because an invitation is a user in an invited state, and the pair is what
// makes that visible. The test asserts the declared event is emitted, not that
// it is the only one.
var registry = map[string]coverage{
	// ── Workspace control plane ─────────────────────────────────────────────
	routeKey("POST", v1Workspaces):           atomic(audit.ActionWorkspaceCreated),
	routeKey("PATCH", v1Workspace):           atomic(audit.ActionWorkspaceRenamed),
	routeKey("POST", v1Workspace+"/archive"): atomic(audit.ActionWorkspaceArchived),

	// ── Connection control plane ────────────────────────────────────────────
	//
	// The most security-sensitive group in the system: these read and write a
	// provider's administrative credential and decide which realm a workspace
	// routes through.
	routeKey("POST", v1Workspace+"/connections"):                         atomic(audit.ActionConnectionCreated),
	routeKey("PATCH", v1Workspace+"/connections/:connection_id"):         atomic(audit.ActionConnectionUpdated),
	routeKey("DELETE", v1Workspace+"/connections/:connection_id"):        atomic(audit.ActionConnectionDeleted),
	routeKey("POST", v1Workspace+"/connections/:connection_id/verify"):   atomic(audit.ActionConnectionVerified),
	routeKey("POST", v1Workspace+"/connections/:connection_id/activate"): atomic(audit.ActionConnectionActivated),
	routeKey("POST", v1Workspace+"/connections/:connection_id/retire"):   atomic(audit.ActionConnectionRetired),

	// ── Project control plane ───────────────────────────────────────────────
	routeKey("POST", v1Workspace+"/projects"):                        atomic(audit.ActionProjectCreated),
	routeKey("PATCH", v1Project):                                     atomic(audit.ActionProjectRenamed),
	routeKey("POST", v1Project+"/archive"):                           atomic(audit.ActionProjectArchived),
	routeKey("POST", v1Project+"/credentials"):                       atomic(audit.ActionCredentialCreated),
	routeKey("POST", v1Project+"/credentials/:credential_id/revoke"): atomic(audit.ActionCredentialRevoked),

	// ── Workspace identity: users ───────────────────────────────────────────
	routeKey("POST", v1Workspace+"/users"):            audited(audit.ActionUserCreated),
	routeKey("PATCH", v1Workspace+"/users/:user_id"):  audited(audit.ActionUserUpdated),
	routeKey("DELETE", v1Workspace+"/users/:user_id"): audited(audit.ActionUserDeleted),

	// Both password paths audit as the same action, and that is correct: what
	// matters afterwards is that somebody changed this user's credential, not
	// which of the two mechanisms they used. The mechanism is in the route,
	// which is in the request log for the same request id.
	routeKey("POST", v1Workspace+"/users/:user_id/reset-password"): audited(audit.ActionUserPasswordReset),
	routeKey("PUT", v1Workspace+"/users/:user_id/password"):        audited(audit.ActionUserPasswordReset),

	// ── Workspace identity: roles ───────────────────────────────────────────
	routeKey("POST", v1Workspace+"/users/:user_id/roles"):              audited(audit.ActionUserRolesGranted),
	routeKey("DELETE", v1Workspace+"/users/:user_id/roles/:role_name"): audited(audit.ActionUserRoleRevoked),
	routeKey("POST", v1Workspace+"/roles"):                             audited(audit.ActionRoleCreated),
	routeKey("PATCH", v1Workspace+"/roles/:role_name"):                 audited(audit.ActionRoleUpdated),
	routeKey("DELETE", v1Workspace+"/roles/:role_name"):                audited(audit.ActionRoleDeleted),

	// ── Workspace identity: sessions ────────────────────────────────────────
	routeKey("DELETE", v1Workspace+"/users/:user_id/sessions"): audited(audit.ActionUserSessionsLoggedOut),
	routeKey("DELETE", v1Workspace+"/sessions/:session_id"):    audited(audit.ActionSessionRevoked),

	// ── Workspace identity: invitations ─────────────────────────────────────
	routeKey("POST", v1Workspace+"/invitations"):                       audited(audit.ActionInvitationCreated),
	routeKey("DELETE", v1Workspace+"/invitations/:invitation_id"):      audited(audit.ActionInvitationRevoked),
	routeKey("POST", v1Workspace+"/invitations/:invitation_id/resend"): audited(audit.ActionInvitationResent),
}

// CoverageFor returns a mutating route's classification.
//
// ok=false means the route has no entry, which the completeness test treats as
// a failure. There is deliberately no default: an unclassified mutation is a
// decision nobody made, and defaulting either way would make it invisible.
func CoverageFor(method, path string) (coverage, bool) {
	c, ok := registry[routeKey(method, path)]
	return c, ok
}

// ClassifiedRoutes returns every classified route, sorted. For tests.
func ClassifiedRoutes() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsMutating reports whether a route changes state.
//
// Derived from the HTTP method rather than from a list, because the method IS
// the declaration: a GET that mutated would be a bug this project would want to
// hear about anyway, and a list would be a third thing to keep in step.
func IsMutating(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}

// ValidateCoverage reports mutating routes with no audit classification.
//
// Takes the mounted set as an argument rather than reaching for it, so the same
// function serves the router and the tests — the shape authz.ValidateRegistry
// established.
func ValidateCoverage(mountedRoutes []string) []string {
	var missing []string
	for _, route := range mountedRoutes {
		method, _, found := strings.Cut(route, " ")
		if !found || !IsMutating(method) {
			continue
		}
		if _, ok := registry[route]; !ok {
			missing = append(missing, route)
		}
	}
	sort.Strings(missing)
	return missing
}

// ControlPlaneRoutes returns every mutation whose state this database owns —
// the ones the transactional guarantee applies to.
//
// Exported because two things outside this package need it and must not derive
// it independently: the atomicity acceptance suite, which asserts one exists
// per operation, and the documentation gate.
func ControlPlaneRoutes() []string {
	out := make([]string, 0, len(registry))
	for key, c := range registry {
		if c.ControlPlane {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// EventFor returns the action a route emits, and whether it is transactional.
func EventFor(method, path string) (action audit.Action, controlPlane bool, ok bool) {
	c, found := registry[routeKey(method, path)]
	if !found {
		return "", false, false
	}
	return c.Event, c.ControlPlane, true
}
