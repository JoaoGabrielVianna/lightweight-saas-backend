// =====================================================
// Router configures all HTTP routes for the application.
//
// This module centralizes route definitions and organizes them into
// public and private route groups. It prepares the structure for
// authentication middleware and feature-specific routes.
//
// Usage:
//
//	SetupRouter(ginEngine, userHandler)
//
// =====================================================

package server

import (
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/gin-gonic/gin"
)

// =====================================================
// SetupRouter configures all HTTP routes.
//
// This function sets up route groups (public and private) and
// registers handlers for each route. It prepares the routing
// structure for adding authentication middleware to private routes.
//
// Parameters:
//   - router: The Gin engine to configure routes on
//   - userHandler: The user authentication handler
//
// =====================================================
func SetupRouter(router *gin.Engine, userHandler *user.Handler) {
	// =====================================================
	// Public Routes
	// Routes accessible without authentication
	// =====================================================
	public := router.Group("/")
	{
		// Authentication endpoints
		public.POST("/register", userHandler.Register)
		public.POST("/login", userHandler.Login)
	}

	// =====================================================
	// Private Routes
	// Routes requiring authentication (future: add middleware)
	// =====================================================
	// private := router.Group("/")
	// private.Use(middleware.AuthRequired())
	// {
	//     Add authenticated routes here
	// }
}
