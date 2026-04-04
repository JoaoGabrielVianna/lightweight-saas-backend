// =====================================================
// DTOs (Data Transfer Objects) for HTTP request/response handling.
//
// This file defines all request and response structures used at the HTTP layer.
// These DTOs are separate from domain models to keep the API contract
// independent from internal domain logic.
//
// =====================================================

package user

import "time"

// =====================================================
// RegisterRequest represents the JSON payload for user registration.
//
// Fields:
//   - Email: User's email address (required)
//   - Password: User's plaintext password (required)
//
// Example:
//
//	{
//	  "email": "test@test.com",
//	  "password": "testPassword"
//	}
//
// =====================================================
type RegisterRequest struct {
	Email    string `json:"email" binding:"required" example:"test@test.com"`
	Password string `json:"password" binding:"required" example:"testPassword"`
}

// =====================================================
// LoginRequest represents the JSON payload for user login.
//
// Fields:
//   - Email: User's email address (required)
//   - Password: User's plaintext password (required)
//
// Example:
//
//	{
//	  "email": "test@test.com",
//	  "password": "testPassword"
//	}
//
// =====================================================
type LoginRequest struct {
	Email    string `json:"email" binding:"required" example:"test@test.com"`
	Password string `json:"password" binding:"required" example:"testPassword"`
}

// =====================================================
// UserResponse represents the safe user data returned in API responses.
//
// This DTO is used to safely serialize user information without exposing
// sensitive fields like password. It includes timestamps for audit purposes.
//
// Fields:
//   - ID: User's unique identifier
//   - Email: User's email address
//   - CreatedAt: Account creation timestamp
//   - UpdatedAt: Last account update timestamp
//
// =====================================================
type UserResponse struct {
	ID        uint      `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// =====================================================
// AuthResponse represents the response returned after successful authentication.
//
// Fields:
//   - User: The authenticated user's information
//   - Token: JWT token for future authenticated requests (currently empty, will be added later)
//
// =====================================================
type AuthResponse struct {
	User  UserResponse `json:"user"`
	Token string       `json:"token"`
}

// =====================================================
// ToUserResponse converts a User domain model to a UserResponse DTO.
//
// This function safely converts the internal domain model to an API response,
// ensuring no sensitive data is exposed.
//
// Parameters:
//   - user: The User domain model
//
// Returns:
//   - UserResponse DTO safe for API responses
//
// =====================================================
func (u *User) ToUserResponse() UserResponse {
	return UserResponse{
		ID:        u.ID,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}
