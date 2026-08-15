// Package server — routing.
//
// Routes are owned by their domain handlers; this file only composes them
// and wires the auth middleware. /register and /login are intentionally
// absent: Keycloak owns identity.
package server

import (
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auditlog"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identityruntime"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/project"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
)

// RouterDeps carries everything the route table is built from.
//
// It replaces the eight positional parameters SetupRouter used to take plus the
// two variadic options bolted on after them ([TD-006]). Four of those
// parameters were nilable and meaningful only by position, and a single
// signature change to them once needed a dedicated commit (e2a3bcd) just to
// repair the call sites it broke. With a struct, adding a surface is additive:
// existing call sites keep compiling, and a reader can tell at the call site
// which dependency is which.
//
// Every field is optional in the sense that nil is a legal value with a
// documented meaning — the zero RouterDeps mounts nothing but the always-on
// routes. That is deliberate: "not configured" is a real deployment state for
// most of these, and it must produce an absent route (404) rather than a
// mounted route that 503s, so an unauthenticated probe cannot confirm a feature
// exists with different config.
//
// This is not a DI container. It is a plain struct with no lookup, no
// registration, and no reflection; the composition root fills it in and passes
// it down exactly once.
//
// [TD-006]: docs/TECH_DEBT.md#td-006
type RouterDeps struct {
	// User backs /me. Always wired — it needs only the database.
	User *user.Handler

	// Identity backs /admin/*. nil ⇒ the entire /admin group is omitted,
	// which is the state of a deployment without admin client credentials.
	Identity *identity.Handler

	// Audit backs /admin/audit-events. nil ⇒ that one route is omitted.
	Audit *AuditHandler

	// Provider validates bearer tokens. Required by every gated group.
	Provider auth.AuthProvider

	// AdminChecker is the GAP-1 live-admin authorization seam. It MUST be
	// non-nil whenever Identity is, and nil disables the live check.
	AdminChecker auth.AdminChecker

	// SMTP and EmailTemplates back realm-settings routes under /admin.
	// nil ⇒ those routes are omitted.
	SMTP           *SMTPHandler
	EmailTemplates *EmailTemplatesHandler

	// Workspace backs /v1/workspaces. nil ⇒ there is no /v1 surface at all,
	// including the surfaces nested under it.
	Workspace *workspace.Handler

	// Connection backs the connections nested under each workspace. nil is
	// the normal state for an installation with no SECRETS_MASTER_KEY, since
	// a provider credential must not be stored without one.
	Connection *connection.Handler

	// Project backs the project + machine-credential surface under each
	// workspace, and the ProjectAuthenticator that lets a credential
	// authenticate at all. nil ⇒ those routes are omitted AND a `lw_sk_` token
	// is refused exactly as an unknown credential would be, with no hint that
	// the feature exists elsewhere.
	Project *project.Handler

	// ProjectAuth resolves project credentials for AuthenticatePrincipal. nil
	// means this installation has no machine-credential surface.
	ProjectAuth auth.ProjectAuthenticator

	// Audit backs GET /v1/workspaces/{id}/audit, the durable trail. nil ⇒ the
	// route is omitted, which is the state of a deployment whose database is
	// not wired — the same nil-means-absent rule every field here follows.
	//
	// Distinct from Audit above, which is the legacy in-process ring behind
	// /admin/audit-events. The two are separate surfaces with separate
	// authority: the ring answers "what just happened on this box", the table
	// answers "what has ever happened in this workspace".
	WorkspaceAudit *auditlog.Handler

	// WorkspaceIdentity backs the workspace-scoped identity API — the routes
	// that resolve each request through a workspace's ACTIVE connection rather
	// than through the process-level Keycloak configuration. nil for the same
	// reason Connection is nil: without a master key there are no connections
	// to route through.
	//
	// This field is the TD-006 refactor paying for itself: it is the first
	// surface added since, and adding it touched no existing call site.
	WorkspaceIdentity *identityruntime.Handler

	// RateLimits tunes the two /v1 limiters. The zero value means "the
	// defaults", so a caller that does not care writes nothing.
	RateLimits RateLimitSettings
}

// RateLimitSettings is the deployment-tunable part of the /v1 rate limits.
//
// Two numbers, not four: burst is derived rather than configured, because the
// two knobs are not independent in any way an operator can reason about. A
// burst below the rate cannot be sustained and a burst far above it turns the
// limiter into an average with no ceiling; twice the rate is the ratio both
// limits already shipped with (10/20 and 20/40) and the one that lets a client
// absorb a second of jitter without buying a second of unmetered traffic.
//
// It is configurable at all because "how much machine traffic is normal" is a
// property of the installation, not of the software: the default suits one
// backend doing routine identity work, and an installation fronting a busy
// product should be able to raise it without a fork. It is NOT a way to switch
// the limiter off — a zero or negative value means "default", never "unlimited",
// so a typo in an env var cannot silently remove the protection.
type RateLimitSettings struct {
	// EdgeRPS is the per-IP allowance at the /v1 edge, in requests per second.
	// It meters unauthenticated and operator traffic; a project credential's
	// token is released after it authenticates. 0 ⇒ rateLimitDefaultRate.
	EdgeRPS float64

	// CredentialRPS is the per-credential allowance, in requests per second,
	// and is the number a machine consumer can actually reach. 0 ⇒
	// credentialRateLimitRate.
	CredentialRPS float64
}

// EdgeBurst is the burst allowance derived from EdgeRPS.
func (s RateLimitSettings) EdgeBurst() int { return burstFor(s.EdgeRPS, rateLimitDefaultBurst) }

// CredentialBurst is the burst allowance derived from CredentialRPS.
func (s RateLimitSettings) CredentialBurst() int {
	return burstFor(s.CredentialRPS, credentialRateLimitBurst)
}

// burstFor is twice the rate, or the module default when the rate is unset.
// Returning the default rather than 0 for an unset rate keeps the pair
// consistent: the middleware would substitute both defaults anyway, and
// computing one from an unset value would produce a burst of 0.
func burstFor(rate float64, fallback int) int {
	if rate <= 0 {
		return fallback
	}
	return int(rate * 2)
}

// SetupRouter mounts all application routes.
//
// Route-group structure:
//
//	public         — /health, /swagger, /dev/auth/* (auth optional or none)
//	private (auth) — every endpoint requires a valid bearer token
//	admin          — private + RequireRole("admin") + RequireLiveAdmin
//
// deps.Identity may be nil when the admin client credentials aren't
// configured. In that case the admin group simply isn't registered — clients
// see 404 (not 403/503) and there's no way for unauthenticated probing to
// confirm the feature would exist with different config.
//
// deps.AdminChecker is the live-admin authorization seam (GAP-1 remediation).
// It MUST be non-nil whenever deps.Identity is non-nil; the wiring layer in
// SetupRoutes builds the cached checker on top of the identity provider and
// passes it through here. Mounting RequireLiveAdmin after RequireRole keeps
// the JWT-claim short-circuit (cheap non-admin denial) and only consults
// Keycloak for tokens whose claim says they SHOULD pass — collapsing the
// out-of-band revocation window from accessTokenLifespan to the cache TTL.
func SetupRouter(router *gin.Engine, deps RouterDeps) {
	// Public routes (none today — Keycloak handles login). Reserved for
	// public health/info endpoints.

	// Private routes — every endpoint past this point requires a valid token.
	//
	// /auth/debug is NOT mounted here: it needs *config.Config to report the
	// expected issuer + allowed-client set, which this function deliberately
	// does not take. Server.SetupRoutes owns that route (it has cfg) and
	// mounts it with RequireAuth applied per-route. Keeping it out of this
	// group is what prevents the duplicate-registration panic that shipped
	// between c4e8329 and this change — see docs/KNOWN_ISSUES.md (KI-001).
	private := router.Group("/")
	private.Use(auth.RequireAuth(deps.Provider))
	{
		private.GET("/me", deps.User.Me)
	}

	// Admin route group — every endpoint under /admin/* requires an
	// authenticated identity AND the realm `admin` role. Only mounted when
	// identity management is configured. Single group + single gate at the
	// group level keeps "did I forget to add RequireRole?" close to zero.
	//
	// Rate-limit (F1 closure): per-IP token bucket sits BEFORE auth so that
	// unauthenticated floods can't burn CPU on JWT validation. Tuned for
	// human admin click-rate — defaults are 10 req/s with burst 20, well
	// above any UI page-load fan-out and well below a scripted DoS.
	if deps.Identity != nil {
		admin := router.Group("/admin")
		admin.Use(RateLimitPerIP(0, 0))
		admin.Use(auth.RequireAuth(deps.Provider))
		admin.Use(auth.RequireRole("admin"))
		if deps.AdminChecker != nil {
			admin.Use(auth.RequireLiveAdmin(deps.AdminChecker))
		}
		{
			// Users
			admin.GET("/users", deps.Identity.ListUsers)
			admin.GET("/users/:id", deps.Identity.GetUser)
			admin.GET("/users/:id/roles", deps.Identity.ListUserRoles)
			admin.GET("/users/:id/sessions", deps.Identity.ListUserSessions)

			// Roles
			admin.GET("/roles", deps.Identity.ListRoles)
			admin.GET("/roles/:name", deps.Identity.GetRole)
			admin.GET("/roles/:name/users", deps.Identity.ListRoleUsers)

			// Sessions
			admin.GET("/sessions", deps.Identity.ListSessions)

			// Invitations
			admin.GET("/invitations", deps.Identity.ListInvitations)

			// ─── Stage 5.2B — CREATE ──────────────────────────────────
			admin.POST("/roles", deps.Identity.CreateRole)
			admin.POST("/invitations", deps.Identity.CreateInvitation)
			// Alias kept for backward compatibility with the spec
			// language ("POST /admin/users/invite") and the existing
			// frontend's Invitations modal. Routes through the same
			// handler — single code path, single audit trail.
			admin.POST("/users/invite", deps.Identity.CreateInvitation)

			// ─── Stage 5.2C — UPDATE ──────────────────────────────────
			admin.PATCH("/users/:id", deps.Identity.UpdateUser)
			admin.PATCH("/roles/:name", deps.Identity.UpdateRole)
			admin.POST("/users/:id/roles", deps.Identity.AssignRolesToUser)
			admin.POST("/users/:id/reset-password", deps.Identity.ResetUserPassword)
			admin.PUT("/users/:id/password", deps.Identity.SetUserPassword)
			admin.POST("/invitations/:id/resend", deps.Identity.ResendInvitation)

			// ─── Stage 5.2D — DELETE ──────────────────────────────────
			admin.DELETE("/users/:id", deps.Identity.DeleteUser)
			admin.DELETE("/users/:id/roles/:name", deps.Identity.UnassignRoleFromUser)
			admin.DELETE("/users/:id/sessions", deps.Identity.LogoutUserSessions)
			admin.DELETE("/roles/:name", deps.Identity.DeleteRole)
			admin.DELETE("/sessions/:id", deps.Identity.DeleteSession)
			admin.DELETE("/invitations/:id", deps.Identity.DeleteInvitation)

			// Observability — in-process audit ring buffer. Read-only.
			// Mounted inside the identity group so the same auth/role/live-
			// admin gates apply; route is omitted entirely when the audit
			// handler hasn't been wired (e.g. tests).
			if deps.Audit != nil {
				admin.GET("/audit-events", deps.Audit.ListEvents)
			}

			// SMTP settings + user provisioning with temp password.
			// Omitted when the SMTP handler isn't wired (no identity provider).
			if deps.SMTP != nil {
				admin.GET("/settings/smtp", deps.SMTP.GetSMTP)
				admin.PUT("/settings/smtp", deps.SMTP.UpdateSMTP)
				admin.POST("/settings/smtp/test", deps.SMTP.TestSMTP)
				admin.POST("/users/password", deps.SMTP.CreateUserWithPassword)
			}

			if deps.EmailTemplates != nil {
				admin.GET("/settings/email-templates", deps.EmailTemplates.GetEmailTemplates)
				admin.PUT("/settings/email-templates/:key", deps.EmailTemplates.UpdateEmailTemplate)
				admin.DELETE("/settings/email-templates/:key", deps.EmailTemplates.ResetEmailTemplate)
			}
		}
	}

	mountV1(router, deps)
}

// mountV1 mounts the versioned public API.
//
// The middleware chain is deliberately identical to /admin/*, in the same
// order, built from the same constructors:
//
//	RateLimitPerIP  — before auth, so an unauthenticated flood cannot burn CPU
//	                  on JWT validation
//	RequireAuth     — valid bearer token
//	RequireRole     — the realm `admin` role, from the token claim (cheap
//	                  short-circuit for the common denial)
//	RequireLiveAdmin— confirms the role is still real, collapsing the stale-JWT
//	                  window (GAP-1). Conditional on adminChecker exactly as in
//	                  the admin group: nil means no identity provider to ask.
//
// /v1 is a separate group rather than a path under /admin because it is the
// product API, not the console's admin surface. It is gated identically today
// because workspace administration is an operator action; when per-workspace
// authorization arrives it will tighten here, never loosen.
//
// requestid.Middleware is mounted on this group ONLY. Mounting it globally
// would add an X-Request-Id header to every /admin/* response, changing a
// surface that must stay byte-compatible.
//
// # The chain, and why it changed in Slice 7
//
// Before projects existed, the group carried
// RequireAuth → RequireRole("admin") → RequireLiveAdmin: one kind of caller,
// authenticated and authorized in one run. A second kind of caller cannot be
// served that way, because the operator checks must not apply to a machine and
// the machine checks must not apply to an operator.
//
// So the chain is split along the seam it always had implicitly:
//
//	AuthenticatePrincipal  — WHO is calling (operator token or project key)
//	RateLimitPerCredential — per-key throttle, needs the answer above
//	Authorize              — MAY this principal perform THIS route
//
// Authorize applies exactly the previous rules to operators (admin role, then
// the live check, in that order so the cheap denial still short-circuits), and
// the workspace binding plus scope to projects. Operator-visible behaviour is
// unchanged; the difference is that the rule now lives in a table that a test
// can prove covers every mounted route.
func mountV1(router *gin.Engine, deps RouterDeps) {
	if deps.Workspace == nil {
		return
	}

	v1 := router.Group("/v1")
	v1.Use(requestid.Middleware())
	v1.Use(RateLimitEdge(deps.RateLimits.EdgeRPS, deps.RateLimits.EdgeBurst()))
	v1.Use(auth.AuthenticatePrincipal(auth.PrincipalConfig{
		Provider: deps.Provider,
		Projects: deps.ProjectAuth,
	}))
	v1.Use(RateLimitPerCredential(deps.RateLimits.CredentialRPS, deps.RateLimits.CredentialBurst()))
	v1.Use(authz.Authorize(authz.Config{AdminChecker: deps.AdminChecker}))
	{
		// The scope vocabulary. Outside the workspace path because it is a
		// property of the installation, and because `/projects/scopes` would
		// collide with `/projects/:project_id` in gin's route tree.
		if deps.Project != nil {
			v1.GET("/project-scopes", deps.Project.Scopes)
		}
		v1.GET("/workspaces", deps.Workspace.List)
		v1.POST("/workspaces", deps.Workspace.Create)
		v1.GET("/workspaces/:workspace_id", deps.Workspace.Get)
		v1.PATCH("/workspaces/:workspace_id", deps.Workspace.Update)
		v1.POST("/workspaces/:workspace_id/archive", deps.Workspace.Archive)

		// Connections are nested under their workspace because that is what
		// they belong to: the workspace id is not decoration, and every
		// handler confirms the connection actually belongs to it before
		// acting. Mounted only when the connection domain is wired, which
		// requires a configured master key.
		if deps.Connection != nil {
			v1.GET("/workspaces/:workspace_id/connections", deps.Connection.List)
			v1.POST("/workspaces/:workspace_id/connections", deps.Connection.Create)
			v1.GET("/workspaces/:workspace_id/connections/:connection_id", deps.Connection.Get)
			v1.PATCH("/workspaces/:workspace_id/connections/:connection_id", deps.Connection.Update)
			v1.DELETE("/workspaces/:workspace_id/connections/:connection_id", deps.Connection.Delete)
			v1.POST("/workspaces/:workspace_id/connections/:connection_id/verify", deps.Connection.Verify)
			v1.POST("/workspaces/:workspace_id/connections/:connection_id/activate", deps.Connection.Activate)
			v1.POST("/workspaces/:workspace_id/connections/:connection_id/retire", deps.Connection.Retire)
		}

		// Projects and their machine credentials. Nested under the workspace
		// because that is what a project belongs to, permanently: the
		// workspace id in the path is not decoration, and the service confirms
		// the project actually belongs to it before acting.
		//
		// Every one of these is OPERATOR ONLY in the authz registry. A
		// credential able to mint credentials would make revocation
		// meaningless — revoke one, and it has already issued another.
		if deps.Project != nil {
			v1.GET("/workspaces/:workspace_id/projects", deps.Project.List)
			v1.POST("/workspaces/:workspace_id/projects", deps.Project.Create)
			v1.GET("/workspaces/:workspace_id/projects/:project_id", deps.Project.Get)
			v1.PATCH("/workspaces/:workspace_id/projects/:project_id", deps.Project.Update)
			v1.POST("/workspaces/:workspace_id/projects/:project_id/archive", deps.Project.Archive)

			// There is deliberately no DELETE: projects are archived, so the
			// `prj_` ids in audit history never become dangling references.
			// And there is no endpoint that returns a credential secret, nor
			// one that rotates a credential — rotation is create-new, deploy,
			// revoke-old, which needs no new state machine.
			v1.GET("/workspaces/:workspace_id/projects/:project_id/credentials", deps.Project.ListCredentials)
			v1.POST("/workspaces/:workspace_id/projects/:project_id/credentials", deps.Project.CreateCredential)
			v1.POST("/workspaces/:workspace_id/projects/:project_id/credentials/:credential_id/revoke", deps.Project.RevokeCredential)
		}

		// Workspace-scoped identity. These are the first routes in the system
		// whose behaviour depends on which Connection a workspace has active:
		// the same path with two different workspace ids reaches two different
		// Keycloak realms, resolved per request.
		//
		// They sit alongside the connection routes rather than under them
		// because a caller managing users should not have to know which
		// connection is serving them — that is the runtime's job, and naming a
		// connection in the path would make it the caller's.
		// The workspace's durable audit trail. Mounted next to the identity
		// routes because it is read with the same credential and bounded by the
		// same workspace, and separately from /admin/audit-events because that
		// one is the process-local ring and has no workspace at all.
		//
		// It is the only route reachable with `audit:read`, and there is no
		// write counterpart: events are emitted by the system as a side effect
		// of other operations, and an endpoint that let a caller author history
		// would make the trail worthless.
		if deps.WorkspaceAudit != nil {
			v1.GET("/workspaces/:workspace_id/audit", deps.WorkspaceAudit.List)
		}

		if deps.WorkspaceIdentity != nil {
			wi := deps.WorkspaceIdentity

			// Users
			v1.GET("/workspaces/:workspace_id/users", wi.ListUsers)
			v1.POST("/workspaces/:workspace_id/users", wi.CreateUser)
			v1.GET("/workspaces/:workspace_id/users/:user_id", wi.GetUser)
			v1.PATCH("/workspaces/:workspace_id/users/:user_id", wi.UpdateUser)
			v1.DELETE("/workspaces/:workspace_id/users/:user_id", wi.DeleteUser)

			// A user's roles
			v1.GET("/workspaces/:workspace_id/users/:user_id/roles", wi.ListUserRoles)
			v1.POST("/workspaces/:workspace_id/users/:user_id/roles", wi.AssignRolesToUser)
			v1.DELETE("/workspaces/:workspace_id/users/:user_id/roles/:role_name", wi.UnassignRoleFromUser)

			// Password operations. Two routes rather than one with a flag:
			// reset-password sends an action email and needs SMTP on the
			// realm; password sets a credential directly and needs nothing.
			// Different prerequisites, different failure modes.
			v1.POST("/workspaces/:workspace_id/users/:user_id/reset-password", wi.ResetUserPassword)
			v1.PUT("/workspaces/:workspace_id/users/:user_id/password", wi.SetUserPassword)

			// A user's sessions
			v1.GET("/workspaces/:workspace_id/users/:user_id/sessions", wi.ListUserSessions)
			v1.DELETE("/workspaces/:workspace_id/users/:user_id/sessions", wi.LogoutUserSessions)

			// Realm-wide sessions
			v1.GET("/workspaces/:workspace_id/sessions", wi.ListSessions)
			v1.DELETE("/workspaces/:workspace_id/sessions/:session_id", wi.DeleteSession)

			// Roles
			v1.GET("/workspaces/:workspace_id/roles", wi.ListRoles)
			v1.POST("/workspaces/:workspace_id/roles", wi.CreateRole)
			v1.GET("/workspaces/:workspace_id/roles/:role_name", wi.GetRole)
			v1.PATCH("/workspaces/:workspace_id/roles/:role_name", wi.UpdateRole)
			v1.DELETE("/workspaces/:workspace_id/roles/:role_name", wi.DeleteRole)
			v1.GET("/workspaces/:workspace_id/roles/:role_name/users", wi.ListRoleUsers)

			// Invitations. Derived from user state rather than a first-class
			// Keycloak resource — see internal/identityruntime's invitation
			// handlers for what that means for a client.
			//
			// There is deliberately NO /v1 equivalent of the legacy
			// POST /admin/users/invite alias: one operation, one route.
			v1.GET("/workspaces/:workspace_id/invitations", wi.ListInvitations)
			v1.POST("/workspaces/:workspace_id/invitations", wi.CreateInvitation)
			v1.DELETE("/workspaces/:workspace_id/invitations/:invitation_id", wi.DeleteInvitation)
			v1.POST("/workspaces/:workspace_id/invitations/:invitation_id/resend", wi.ResendInvitation)
		}
	}

	assertEveryV1RouteIsClassified(router)
}

// assertEveryV1RouteIsClassified fails the boot when a mounted /v1 route has no
// authorization classification.
//
// It reads the routes gin actually registered rather than a hand-kept list, so
// it cannot be satisfied by updating a declaration and forgetting the route, or
// vice versa. A missing entry means nobody decided whether a machine credential
// may reach that endpoint, and the honest response to that is to refuse to
// start — Authorize would deny the route at runtime anyway, and discovering
// that as a mysterious 403 in production is strictly worse than discovering it
// here.
//
// Panic rather than a logged warning, for the same reason gin panics on a route
// conflict: it is a programming error, it is deterministic, and it is caught by
// every test that builds a router.
func assertEveryV1RouteIsClassified(router *gin.Engine) {
	var mounted []string
	for _, r := range router.Routes() {
		if strings.HasPrefix(r.Path, "/v1/") || r.Path == "/v1" {
			mounted = append(mounted, r.Method+" "+r.Path)
		}
	}
	if err := authz.ValidateRegistry(mounted); err != nil {
		panic(err.Error())
	}
}
