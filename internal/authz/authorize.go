package authz

import (
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

var log = logger.New("authz")

// Config is what Authorize needs.
type Config struct {
	// AdminChecker performs the live admin lookup for operators. nil disables
	// the live check, matching the existing behaviour of the /admin group and
	// of the pre-Slice-7 /v1 group: nil means there is no identity provider to
	// ask, not that the check is optional.
	AdminChecker auth.AdminChecker
}

// Authorize is the single authorization boundary for /v1.
//
// It is mounted ONCE on the group. It does not need to be repeated per route,
// and per-route middleware was rejected precisely because a route added without
// it would be silently unguarded. Instead the route's own registered pattern —
// gin's c.FullPath(), which is exactly the key format the registry and
// identityruntime.MountedWorkspaceIdentityRoutes use — is looked up in the
// registry, and an unclassified route is refused.
//
// # Order, and what each step buys
//
//  1. resolve the principal            (put there by AuthenticatePrincipal)
//  2. look up the route's requirement  (unclassified ⇒ deny)
//  3. operator  → realm admin role, then live admin
//     project   → workspace binding, then scope
//
// For a project, both checks are pure comparisons over values already in
// memory, and both run BEFORE the handler — which means before the resolver
// loads a workspace, before a connection is read, before a sealed provider
// credential is opened, and before any traffic reaches the provider. That
// ordering is the central security property of this slice, not an optimisation:
//
//	project bound to A + request for B  ⇒  403, and B is never touched.
//
// The binding check also cannot leak existence. It compares the path id against
// the id on the principal, so the answer is identical whether workspace B
// exists, is archived, or was never created.
func Authorize(cfg Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.PrincipalFrom(c)
		if !ok || principal == nil {
			// Defensive: AuthenticatePrincipal must have run. If a group is
			// ever assembled without it, deny rather than assume.
			deny(c, ErrUnauthenticated)
			return
		}

		route := c.FullPath()
		req, classified := RequirementFor(c.Request.Method, route)
		if !classified {
			// Fail closed, and say so loudly. A mounted-but-unclassified route
			// is a security decision nobody made; ValidateRegistry turns this
			// into a boot failure, and this branch is what happens if a route
			// somehow escapes that check at runtime.
			log.Error("no authorization classification for route " + c.Request.Method + " " + route +
				" (request_id=" + requestid.FromContext(c) + "); denying")
			deny(c, ErrForbidden)
			return
		}

		switch {
		case principal.IsOperator():
			authorizeOperator(c, cfg, principal.Operator)
		case principal.IsProject():
			authorizeProject(c, req, principal.Project)
		default:
			deny(c, ErrUnauthenticated)
		}
	}
}

// authorizeOperator applies exactly the rules the /v1 group applied before this
// slice: the realm `admin` role from the token claim, then the live check that
// collapses the stale-JWT window (GAP-1).
//
// The order is load-bearing and is preserved: the claim check is a cheap local
// denial, and the live check costs a Keycloak round trip, so a caller without
// the claim must never reach it. TestSetupRouter_V1RequiresAdminRole pins that
// the checker is not called on a claim denial.
//
// OperatorOnly vs scoped makes no difference here. An operator is authorized
// for the whole /v1 surface by being a live realm admin; scopes describe what a
// MACHINE may do, and giving operators a second, parallel permission model
// would be a new RBAC system, which this slice explicitly does not build.
func authorizeOperator(c *gin.Context, cfg Config, id *auth.Identity) {
	if !id.HasRole(operatorRole) {
		auth.EmitEvent(auth.AuthEvent{
			Kind:    auth.EventForbidden,
			Subject: id.Subject,
			Reason:  "missing role: " + operatorRole,
			Path:    c.Request.URL.Path,
			Method:  c.Request.Method,
		})
		deny(c, ErrForbidden)
		return
	}

	if cfg.AdminChecker != nil {
		isAdmin, err := cfg.AdminChecker.IsAdmin(c.Request.Context(), id.Subject)
		if err != nil {
			auth.EmitEvent(auth.AuthEvent{
				Kind:    auth.EventForbidden,
				Subject: id.Subject,
				Reason:  "live admin check failed: " + err.Error(),
				Path:    c.Request.URL.Path,
				Method:  c.Request.Method,
			})
			// Fail closed: an admin verb never runs on a guess.
			deny(c, ErrAuthorizationUnavailable)
			return
		}
		if !isAdmin {
			auth.EmitEvent(auth.AuthEvent{
				Kind:    auth.EventForbidden,
				Subject: id.Subject,
				Reason:  "live admin check denied: token role no longer present server-side",
				Path:    c.Request.URL.Path,
				Method:  c.Request.Method,
			})
			deny(c, ErrForbidden)
			return
		}
	}
	c.Next()
}

// operatorRole is the realm role an operator must hold. Kept as a constant here
// so the value is greppable alongside the rule that uses it; it matches the
// string the /admin group passes to RequireRole.
const operatorRole = "admin"

// authorizeProject applies the two machine checks, binding first.
//
// Binding before scope is deliberate. Both are free, so the ordering is about
// what the answer reveals: a credential probing another workspace learns only
// "not yours", never which scopes would have been needed there.
func authorizeProject(c *gin.Context, req Requirement, p *auth.ProjectPrincipal) {
	if req.OperatorOnly {
		denyProject(c, p, ErrOperatorOnly)
		return
	}

	// ── The workspace boundary ──────────────────────────────────────────────
	//
	// Routes with no :workspace_id are all operator-only, so reaching here
	// without one would mean the registry classified a workspace-less route as
	// scoped. Deny rather than skip the check: a scoped route that cannot be
	// bound to a workspace is a registry bug, and letting it through would make
	// the bug invisible.
	pathWorkspace := c.Param("workspace_id")
	if pathWorkspace == "" {
		log.Error("scoped route " + c.FullPath() + " has no workspace_id parameter; denying")
		denyProject(c, p, ErrForbidden)
		return
	}
	if !sameWorkspace(p.WorkspaceID, pathWorkspace) {
		denyProject(c, p, ErrWorkspaceMismatch)
		return
	}

	// ── The capability ──────────────────────────────────────────────────────
	if !HasScope(p.Scopes, req.Scope) {
		// RFC 6750 §3.1: name the scope that would have been required. This is
		// safe to disclose — the caller already knows which endpoint it called
		// and is inside its own workspace — and it is the difference between a
		// developer fixing their key in a minute and filing a ticket.
		c.Header("WWW-Authenticate",
			`Bearer error="insufficient_scope", scope="`+string(req.Scope)+`"`)
		denyProject(c, p, ErrInsufficientScope)
		return
	}

	c.Next()
}

// sameWorkspace compares the principal's binding against the path.
//
// Both sides are normalized through publicid.Parse so `ws_<uuid>` and the bare
// UUID form (which Parse accepts, and which curl users do use) cannot be made to
// disagree by spelling. A malformed path id fails the comparison rather than
// erroring separately: for a project the answer is the same either way, and
// producing invalid_workspace_id here would let a credential probe which ids
// are well-formed before authorization ran.
func sameWorkspace(bound, fromPath string) bool {
	a, err := publicid.Parse(publicid.WorkspacePrefix, bound)
	if err != nil {
		return false
	}
	b, err := publicid.Parse(publicid.WorkspacePrefix, fromPath)
	if err != nil {
		return false
	}
	return a == b
}

// denyProject records the refusal and writes it.
//
// Project denials DO reach the security event channel: unlike a failed
// authentication, this is a real, identified principal doing something
// unexpected, it is low-volume, and it is the signal an operator needs to spot
// a misconfigured or misbehaving backend.
func denyProject(c *gin.Context, p *auth.ProjectPrincipal, e *Error) {
	auth.EmitEvent(auth.AuthEvent{
		Kind:    auth.EventForbidden,
		Subject: p.ProjectID,
		Reason:  e.Code + " (credential=" + p.CredentialID + ", bound=" + p.WorkspaceID + ")",
		Path:    c.Request.URL.Path,
		Method:  c.Request.Method,
	})
	deny(c, e)
}

func deny(c *gin.Context, e *Error) {
	c.AbortWithStatusJSON(e.Status, ErrorResponse{Error: ErrorBody{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: requestid.FromContext(c),
	}})
}

// RequireOperator is a standalone guard for surfaces mounted outside the /v1
// group that must never accept a project credential.
//
// Nothing uses it today — every /v1 route is classified in the registry, and
// /admin/* never runs AuthenticatePrincipal at all. It exists so that a future
// surface which does authenticate principals has an obvious, named way to be
// operator-only without inventing its own check.
func RequireOperator() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFrom(c)
		if !ok || p == nil {
			deny(c, ErrUnauthenticated)
			return
		}
		if !p.IsOperator() {
			deny(c, ErrOperatorOnly)
			return
		}
		c.Next()
	}
}

// statusOK is a readability helper for tests asserting the middleware passed.
var _ = http.StatusOK
