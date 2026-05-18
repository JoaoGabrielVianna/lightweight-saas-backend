// Package server — routing.
//
// Routes are owned by their domain handlers; this file only composes them
// and wires the auth middleware. /register and /login are intentionally
// absent: Keycloak owns identity.
package server

import (
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/gin-gonic/gin"
)

// SetupRouter mounts all application routes. The auth provider validates
// every protected request via auth.RequireAuth.
func SetupRouter(router *gin.Engine, userHandler *user.Handler, provider auth.AuthProvider) {
	// Public routes (none today — Keycloak handles login). Reserved for
	// public health/info endpoints.

	// Private routes — every endpoint past this point requires a valid token.
	private := router.Group("/")
	private.Use(auth.RequireAuth(provider))
	{
		private.GET("/me", userHandler.Me)
	}
}
