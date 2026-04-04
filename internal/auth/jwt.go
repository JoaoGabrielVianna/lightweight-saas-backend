// =====================================================
// Package auth handles JWT token generation and validation.
//
// This package provides utilities for creating and managing
// JWT tokens for user authentication in protected routes.
//
// =====================================================
package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// =====================================================
// Claims represents the JWT claims structure.
//
// Fields:
//   - UserID: The user's unique identifier
//   - jwt.RegisteredClaims: Standard JWT claims (exp, iat, etc)
//
// =====================================================
type Claims struct {
	UserID uint `json:"user_id"`
	jwt.RegisteredClaims
}

// =====================================================
// GenerateToken creates a signed JWT token for a user.
//
// Parameters:
//   - userID: The user's unique identifier
//   - secret: The JWT secret key for signing
//
// Returns:
//   - string: The signed JWT token
//   - error: Any error that occurred during generation
//
// Token expires in 24 hours from generation time.
//
// =====================================================
func GenerateToken(userID uint, secret string) (string, error) {
	now := time.Now()

	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// =====================================================
// ValidateToken parses and validates a JWT token string.
//
// Parameters:
//   - tokenString: The JWT token string to validate
//   - secret: The JWT secret key used for verification
//
// Returns:
//   - *Claims: The decoded claims if valid
//   - error: Any error that occurred during validation
//
// =====================================================
func ValidateToken(tokenString string, secret string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}
