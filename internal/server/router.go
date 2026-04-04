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
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/gin-gonic/gin"
)

// =====================================================
// AuthMiddleware validates JWT tokens and extracts user information.
//
// This middleware expects a Bearer token in the Authorization header,
// validates it using JWT, and stores the user_id in the Gin context
// for use in protected route handlers.
//
// Expected header format:
//
//	Authorization: Bearer <JWT_TOKEN>
//
// Returns:
//   - gin.HandlerFunc: Middleware handler
//
// =====================================================
func AuthMiddleware(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			log.Error("missing authorization header")
			c.JSON(401, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		// Log for debug
		log.Info("Authorization header received: " + authHeader[:20] + "...")

		// Extract Bearer token
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			log.Error("invalid authorization header format")
			c.JSON(401, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		tokenString := strings.TrimPrefix(authHeader, bearerPrefix)
		if tokenString == "" {
			log.Error("empty token")
			c.JSON(401, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		// Validate JWT token
		claims, err := auth.ValidateToken(tokenString, jwtSecret)
		if err != nil {
			log.Error("token validation failed: " + err.Error())
			c.JSON(401, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		// Extract user_id from claims
		if claims.UserID == 0 {
			log.Error("invalid user_id in claims")
			c.JSON(401, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		// Store user_id in context for handlers
		c.Set("user_id", claims.UserID)
		log.Info("token validated for user: " + string(rune(claims.UserID)))

		c.Next()
	}
}

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
//   - jwtSecret: Secret key for JWT validation
//
// =====================================================
func SetupRouter(router *gin.Engine, userHandler *user.Handler, jwtSecret string) {
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
	// Routes requiring authentication (middleware: AuthRequired)
	// =====================================================
	private := router.Group("/")
	private.Use(AuthMiddleware(jwtSecret))
	{
		// User endpoints
		private.GET("/me", userHandler.Me)
	}
}
