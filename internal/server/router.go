// Package server — routing.
//
// Routes are owned by their domain handlers; this file only composes them
// and wires the auth middleware. /register and /login are intentionally
// absent: Keycloak owns identity.
package server

import (
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/gin-gonic/gin"
)

// SetupRouter mounts all application routes.
//
// Route-group structure:
//
//	public         — /health, /swagger, /dev/auth/* (auth optional or none)
//	private (auth) — every endpoint requires a valid bearer token
//	admin          — private + RequireRole("admin") + RequireLiveAdmin
//
// identityHandler may be nil when the admin client credentials aren't
// configured. In that case the admin group simply isn't registered — clients
// see 404 (not 403/503) and there's no way for unauthenticated probing to
// confirm the feature would exist with different config.
//
// adminChecker is the live-admin authorization seam (GAP-1 remediation). It
// MUST be non-nil whenever identityHandler is non-nil; the wiring layer in
// SetupRoutes builds the cached checker on top of the identity provider and
// passes it through here. Mounting RequireLiveAdmin after RequireRole keeps
// the JWT-claim short-circuit (cheap non-admin denial) and only consults
// Keycloak for tokens whose claim says they SHOULD pass — collapsing the
// out-of-band revocation window from accessTokenLifespan to the cache TTL.
func SetupRouter(router *gin.Engine, userHandler *user.Handler, identityHandler *identity.Handler, auditHandler *AuditHandler, provider auth.AuthProvider, adminChecker auth.AdminChecker, smtpHandler *SMTPHandler) {
	// Public routes (none today — Keycloak handles login). Reserved for
	// public health/info endpoints.

	// Private routes — every endpoint past this point requires a valid token.
	private := router.Group("/")
	private.Use(auth.RequireAuth(provider))
	{
		private.GET("/me", userHandler.Me)
		private.GET("/auth/debug", func(c *gin.Context) {
			id, _ := auth.IdentityFrom(c)
			azp, _ := id.Raw["azp"].(string)
			c.JSON(200, gin.H{
				"received_sub": id.Subject,
				"received_azp": azp,
				"email":        id.Email,
				"username":     id.Username,
				"roles":        id.Roles,
				"expires_at":   id.ExpiresAt,
			})
		})
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
	if identityHandler != nil {
		admin := router.Group("/admin")
		admin.Use(RateLimitPerIP(0, 0))
		admin.Use(auth.RequireAuth(provider))
		admin.Use(auth.RequireRole("admin"))
		if adminChecker != nil {
			admin.Use(auth.RequireLiveAdmin(adminChecker))
		}
		{
			// Users
			admin.GET("/users", identityHandler.ListUsers)
			admin.GET("/users/:id", identityHandler.GetUser)
			admin.GET("/users/:id/roles", identityHandler.ListUserRoles)
			admin.GET("/users/:id/sessions", identityHandler.ListUserSessions)

			// Roles
			admin.GET("/roles", identityHandler.ListRoles)
			admin.GET("/roles/:name", identityHandler.GetRole)
			admin.GET("/roles/:name/users", identityHandler.ListRoleUsers)

			// Sessions
			admin.GET("/sessions", identityHandler.ListSessions)

			// Invitations
			admin.GET("/invitations", identityHandler.ListInvitations)

			// ─── Stage 5.2B — CREATE ──────────────────────────────────
			admin.POST("/roles", identityHandler.CreateRole)
			admin.POST("/invitations", identityHandler.CreateInvitation)
			// Alias kept for backward compatibility with the spec
			// language ("POST /admin/users/invite") and the existing
			// frontend's Invitations modal. Routes through the same
			// handler — single code path, single audit trail.
			admin.POST("/users/invite", identityHandler.CreateInvitation)

			// ─── Stage 5.2C — UPDATE ──────────────────────────────────
			admin.PATCH("/users/:id", identityHandler.UpdateUser)
			admin.PATCH("/roles/:name", identityHandler.UpdateRole)
			admin.POST("/users/:id/roles", identityHandler.AssignRolesToUser)
			admin.POST("/users/:id/reset-password", identityHandler.ResetUserPassword)
			admin.POST("/invitations/:id/resend", identityHandler.ResendInvitation)

			// ─── Stage 5.2D — DELETE ──────────────────────────────────
			admin.DELETE("/users/:id", identityHandler.DeleteUser)
			admin.DELETE("/users/:id/roles/:name", identityHandler.UnassignRoleFromUser)
			admin.DELETE("/users/:id/sessions", identityHandler.LogoutUserSessions)
			admin.DELETE("/roles/:name", identityHandler.DeleteRole)
			admin.DELETE("/sessions/:id", identityHandler.DeleteSession)
			admin.DELETE("/invitations/:id", identityHandler.DeleteInvitation)

			// Observability — in-process audit ring buffer. Read-only.
			// Mounted inside the identity group so the same auth/role/live-
			// admin gates apply; route is omitted entirely when the audit
			// handler hasn't been wired (e.g. tests).
			if auditHandler != nil {
				admin.GET("/audit-events", auditHandler.ListEvents)
			}

			// SMTP settings + user provisioning with temp password.
			// Omitted when the SMTP handler isn't wired (no identity provider).
			if smtpHandler != nil {
				admin.GET("/settings/smtp", smtpHandler.GetSMTP)
				admin.PUT("/settings/smtp", smtpHandler.UpdateSMTP)
				admin.POST("/settings/smtp/test", smtpHandler.TestSMTP)
				admin.POST("/users/password", smtpHandler.CreateUserWithPassword)
			}
		}
	}
}
