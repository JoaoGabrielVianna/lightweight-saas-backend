// =====================================================
// Middleware provides authentication middleware for protected routes.
//
// This module implements Gin middleware that validates JWT tokens
// and extracts user information from claims.
//
// =====================================================
package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// =====================================================
// AuthMiddleware returns a Gin middleware that validates JWT tokens.
//
// Behavior:
//   - Reads the Authorization header
//   - Expects format: "Bearer <token>"
//   - Validates the token using the provided secret
//   - Extracts user_id from claims and stores in context
//   - Returns 401 Unauthorized if token is missing or invalid
//
// Parameters:
//   - secret: The JWT secret key for token validation
//
// Returns:
//   - gin.HandlerFunc: The middleware function for Gin
//
// Usage:
//
//	router.POST("/protected", auth.AuthMiddleware(cfg.JWTSecret), handler)
//
// =====================================================
func AuthMiddleware(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "missing authorization header",
			})
			c.Abort()
			return
		}

		// Parse Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid authorization header format",
			})
			c.Abort()
			return
		}

		tokenString := parts[1]

		// Validate and parse token
		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			// Ensure the signing method is HS256
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid or expired token",
			})
			c.Abort()
			return
		}

		// Extract user_id from claims and store in context
		c.Set("user_id", claims.UserID)
		c.Next()
	}
}

// =====================================================
// GetUserID extracts the user_id from Gin context.
//
// This helper function retrieves the user_id that was stored
// by AuthMiddleware. It should only be called within protected routes.
//
// Parameters:
//   - c: The Gin context
//
// Returns:
//   - uint: The user's ID (0 if not found)
//   - bool: True if user_id was found, false otherwise
//
// =====================================================
func GetUserID(c *gin.Context) (uint, bool) {
	userID, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}

	id, ok := userID.(uint)
	return id, ok
}
