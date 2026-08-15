package authz

import (
	"fmt"
	"sort"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identityruntime"
)

// Requirement is one route's authorization classification.
//
// A route is either OPERATOR ONLY, or reachable by a project credential holding
// a named scope. There is no third state, and there is deliberately no default:
// a route with no entry in the registry is refused at runtime and fails a test
// at build time. That is the whole point of the registry — a route added in a
// future slice cannot reach the provider without someone having made an
// explicit security decision about it.
type Requirement struct {
	// OperatorOnly means no project credential may reach this route, whatever
	// scopes it holds.
	OperatorOnly bool

	// Scope is the capability a project credential must hold. Empty when
	// OperatorOnly.
	Scope Scope
}

// operatorOnly is the classification for control-plane routes.
func operatorOnly() Requirement { return Requirement{OperatorOnly: true} }

// scoped is the classification for routes a project may reach.
func scoped(s Scope) Requirement { return Requirement{Scope: s} }

// routeKey is "METHOD /path/with/:params", matching gin's registered pattern
// exactly as c.FullPath() reports it and as
// identityruntime.MountedWorkspaceIdentityRoutes declares it.
//
// Using the gin pattern rather than the concrete URL is what makes the lookup a
// map hit with no parsing, and what lets the completeness test compare the
// registry against the mounted route table directly.
func routeKey(method, fullPath string) string { return method + " " + fullPath }

const (
	v1Workspaces = "/v1/workspaces"
	v1Workspace  = "/v1/workspaces/:workspace_id"
	v1Project    = v1Workspace + "/projects/:project_id"
)

// registry is the complete authorization table for /v1.
//
// Read it as the security specification it is: every line is a decision about
// whether a machine credential may perform that operation, and the file is the
// only place that decision lives.
var registry = map[string]Requirement{
	// ── Workspace management ────────────────────────────────────────────────
	// Operator only, without exception. A project administers identities
	// INSIDE a workspace; it never administers the workspace. Listing would
	// also enumerate other tenants' workspaces, which a project must not see.
	routeKey("GET", v1Workspaces):            operatorOnly(),
	routeKey("POST", v1Workspaces):           operatorOnly(),
	routeKey("GET", v1Workspace):             operatorOnly(),
	routeKey("PATCH", v1Workspace):           operatorOnly(),
	routeKey("POST", v1Workspace+"/archive"): operatorOnly(),

	// ── Connection management ───────────────────────────────────────────────
	// Operator only. These read and write the provider's ADMINISTRATIVE
	// credential and decide which realm the workspace routes through; a
	// project able to create a connection would escalate to full control of
	// any realm it could reach.
	routeKey("GET", v1Workspace+"/connections"):                          operatorOnly(),
	routeKey("POST", v1Workspace+"/connections"):                         operatorOnly(),
	routeKey("GET", v1Workspace+"/connections/:connection_id"):           operatorOnly(),
	routeKey("PATCH", v1Workspace+"/connections/:connection_id"):         operatorOnly(),
	routeKey("DELETE", v1Workspace+"/connections/:connection_id"):        operatorOnly(),
	routeKey("POST", v1Workspace+"/connections/:connection_id/verify"):   operatorOnly(),
	routeKey("POST", v1Workspace+"/connections/:connection_id/activate"): operatorOnly(),
	routeKey("POST", v1Workspace+"/connections/:connection_id/retire"):   operatorOnly(),

	// ── Project management ──────────────────────────────────────────────────
	// Operator only. A credential that could mint credentials would make
	// revocation meaningless: revoke one, and it has already issued another.
	routeKey("GET", v1Workspace+"/projects"):                         operatorOnly(),
	routeKey("POST", v1Workspace+"/projects"):                        operatorOnly(),
	routeKey("GET", v1Project):                                       operatorOnly(),
	routeKey("PATCH", v1Project):                                     operatorOnly(),
	routeKey("POST", v1Project+"/archive"):                           operatorOnly(),
	routeKey("GET", v1Project+"/credentials"):                        operatorOnly(),
	routeKey("POST", v1Project+"/credentials"):                       operatorOnly(),
	routeKey("POST", v1Project+"/credentials/:credential_id/revoke"): operatorOnly(),

	// The scope vocabulary. Outside the workspace path because it is a property
	// of the installation, and because `/projects/scopes` would collide with
	// `/projects/:project_id` in the router's tree.
	routeKey("GET", "/v1/project-scopes"): operatorOnly(),

	// ── Workspace identity: users ───────────────────────────────────────────
	routeKey("GET", v1Workspace+"/users"):             scoped(ScopeUsersRead),
	routeKey("POST", v1Workspace+"/users"):            scoped(ScopeUsersWrite),
	routeKey("GET", v1Workspace+"/users/:user_id"):    scoped(ScopeUsersRead),
	routeKey("PATCH", v1Workspace+"/users/:user_id"):  scoped(ScopeUsersWrite),
	routeKey("DELETE", v1Workspace+"/users/:user_id"): scoped(ScopeUsersWrite),

	// Password operations. The two are classified differently ON PURPOSE.
	//
	// reset-password dispatches the provider's action email: the user ends up
	// choosing their own credential, and a compromised project key cannot read
	// the mailbox. A backend legitimately needs this.
	//
	// PUT .../password sets a credential directly, with no email and no
	// consent. That is a complete account-takeover primitive, and no backend
	// use case needs it that reset-password does not already serve. Excluding
	// it now costs nothing; including it could never be walked back, because
	// every key issued under the looser rule would keep the capability.
	routeKey("POST", v1Workspace+"/users/:user_id/reset-password"): scoped(ScopeUsersWrite),
	routeKey("PUT", v1Workspace+"/users/:user_id/password"):        operatorOnly(),

	// ── Workspace identity: user roles ──────────────────────────────────────
	// Granting a role is classified under roles:write, not users:write. What
	// is sensitive is the privilege being handed out, not the user record, so
	// an operator can give a backend the ability to manage profiles without
	// also giving it the ability to hand out privileges.
	routeKey("GET", v1Workspace+"/users/:user_id/roles"):               scoped(ScopeRolesRead),
	routeKey("POST", v1Workspace+"/users/:user_id/roles"):              scoped(ScopeRolesWrite),
	routeKey("DELETE", v1Workspace+"/users/:user_id/roles/:role_name"): scoped(ScopeRolesWrite),

	// ── Workspace identity: sessions ────────────────────────────────────────
	routeKey("GET", v1Workspace+"/users/:user_id/sessions"):    scoped(ScopeSessionsRead),
	routeKey("DELETE", v1Workspace+"/users/:user_id/sessions"): scoped(ScopeSessionsRevoke),
	routeKey("GET", v1Workspace+"/sessions"):                   scoped(ScopeSessionsRead),
	routeKey("DELETE", v1Workspace+"/sessions/:session_id"):    scoped(ScopeSessionsRevoke),

	// ── Workspace identity: roles ───────────────────────────────────────────
	routeKey("GET", v1Workspace+"/roles"):                  scoped(ScopeRolesRead),
	routeKey("POST", v1Workspace+"/roles"):                 scoped(ScopeRolesWrite),
	routeKey("GET", v1Workspace+"/roles/:role_name"):       scoped(ScopeRolesRead),
	routeKey("PATCH", v1Workspace+"/roles/:role_name"):     scoped(ScopeRolesWrite),
	routeKey("DELETE", v1Workspace+"/roles/:role_name"):    scoped(ScopeRolesWrite),
	routeKey("GET", v1Workspace+"/roles/:role_name/users"): scoped(ScopeRolesRead),

	// ── Workspace audit ─────────────────────────────────────────────────────
	//
	// Read-only, and the only route reachable with `audit:read`. There is no
	// write counterpart by design: events are emitted by the system as a side
	// effect of other operations, and an endpoint that let a caller author
	// history would make the trail worthless.
	routeKey("GET", v1Workspace+"/audit"): scoped(ScopeAuditRead),

	// ── Workspace identity: invitations ─────────────────────────────────────
	routeKey("GET", v1Workspace+"/invitations"):                        scoped(ScopeInvitationsRead),
	routeKey("POST", v1Workspace+"/invitations"):                       scoped(ScopeInvitationsWrite),
	routeKey("DELETE", v1Workspace+"/invitations/:invitation_id"):      scoped(ScopeInvitationsWrite),
	routeKey("POST", v1Workspace+"/invitations/:invitation_id/resend"): scoped(ScopeInvitationsWrite),
}

// RequirementFor returns the classification for a mounted route.
//
// ok=false means the route has no entry, which Authorize treats as a denial
// rather than as permission. Failing closed here is what makes the registry a
// security control rather than documentation: forgetting an entry costs a 403
// and a loud log line, not an open door.
func RequirementFor(method, fullPath string) (Requirement, bool) {
	r, ok := registry[routeKey(method, fullPath)]
	return r, ok
}

// RegisteredRoutes returns every classified route, sorted. Used by tests and by
// the boot-time completeness check.
func RegisteredRoutes() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateRegistry reports routes that are mounted but unclassified.
//
// Called at boot by the composition root so a missing classification stops the
// process rather than waiting to be discovered as a mysterious 403 in
// production. It takes the mounted set as an argument rather than reaching for
// it, so the same function serves the router, the tests, and any future surface.
func ValidateRegistry(mountedRoutes []string) error {
	var missing []string
	for _, r := range mountedRoutes {
		if _, ok := registry[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("authz: %d mounted route(s) have no authorization classification: %v",
			len(missing), missing)
	}
	return nil
}

// IdentityRoutes re-exports the workspace-identity route declaration so callers
// validating the registry do not have to import identityruntime for one call.
//
// The dependency direction is authz → identityruntime, never the reverse:
// identityruntime must not know how it is authorized, or the write guard and
// the capability check would become two halves of one tangled rule.
func IdentityRoutes() []string { return identityruntime.MountedWorkspaceIdentityRoutes() }
