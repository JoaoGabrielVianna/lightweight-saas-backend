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
//	admin          — private + RequireRole("admin")
//
// identityHandler may be nil when the admin client credentials aren't
// configured. In that case the admin group simply isn't registered — clients
// see 404 (not 403/503) and there's no way for unauthenticated probing to
// confirm the feature would exist with different config.
func SetupRouter(router *gin.Engine, userHandler *user.Handler, identityHandler *identity.Handler, provider auth.AuthProvider) {
	// Public routes (none today — Keycloak handles login). Reserved for
	// public health/info endpoints.

	// Private routes — every endpoint past this point requires a valid token.
	private := router.Group("/")
	private.Use(auth.RequireAuth(provider))
	{
		private.GET("/me", userHandler.Me)
	}

	// Admin route group — every endpoint under /admin/* requires an
	// authenticated identity AND the realm `admin` role. Only mounted when
	// identity management is configured. Single group + single gate at the
	// group level keeps "did I forget to add RequireRole?" close to zero.
	if identityHandler != nil {
		admin := router.Group("/admin")
		admin.Use(auth.RequireAuth(provider))
		admin.Use(auth.RequireRole("admin"))
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
		}
	}
}
