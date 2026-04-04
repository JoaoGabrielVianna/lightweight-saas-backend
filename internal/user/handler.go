// =====================================================
// Handler provides HTTP endpoints for user authentication.
//
// This layer exposes the authentication service through HTTP endpoints
// with proper request/response handling and Swagger documentation.
//
// Usage:
//
//	handler := user.NewHandler(service)
//	r := gin.Default()
//	r.POST("/register", handler.Register)
//	r.POST("/login", handler.Login)
//
// =====================================================

package user

import (
	"fmt"
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/gin-gonic/gin"
)

var log = logger.New("user-handler")

// =====================================================
// Handler provides HTTP endpoints for authentication.
//
// This struct wraps the authentication service and provides
// HTTP request/response handling through Gin.
//
// Fields:
//   - service: The user service for business logic
//   - jwtSecret: Secret key for JWT token signing
//
// =====================================================
type Handler struct {
	service   *Service
	jwtSecret string
}

// =====================================================
// NewHandler creates a new authentication handler.
//
// This constructor initializes a handler with the provided
// service instance and JWT secret for token generation.
//
// Parameters:
//   - service: The user service instance
//   - jwtSecret: Secret key for JWT signing
//
// Returns:
//   - A new Handler instance
//
// =====================================================
func NewHandler(service *Service, jwtSecret string) *Handler {
	return &Handler{
		service:   service,
		jwtSecret: jwtSecret,
	}
}

// =====================================================
// Register creates a new user account.
//
// @Summary Register user
// @Description Create a new user account with email and password
// @Description
// @Description Test credentials:
// @Description - Email: test@test.com
// @Description - Password: testPassword
// @Tags auth
// @Accept json
// @Produce json
// @Param body body RegisterRequest true "User credentials"
// @Success 201 {object} UserResponse
// @Failure 400 {object} map[string]string
// @Router /register [post]
//
// This endpoint accepts email and password, validates them,
// hashes the password, and creates a new user in the database.
//
// Parameters:
//   - c: Gin context for request/response handling
//
// =====================================================
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error("failed to parse register request: " + err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	user, err := h.service.Register(req.Email, req.Password)
	if err != nil {
		if err == ErrUserAlreadyExists {
			log.Error("registration failed: user already exists - " + req.Email)
			c.JSON(http.StatusBadRequest, gin.H{"error": "user already exists"})
			return
		}

		log.Error("registration failed for " + req.Email + ": " + err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": "registration failed"})
		return
	}

	response := user.ToUserResponse()
	c.JSON(http.StatusCreated, response)
}

// =====================================================
// Login authenticates a user.
//
// @Summary Login user
// @Description Authenticate user with email and password
// @Description
// @Description Test credentials:
// @Description - Email: test@test.com
// @Description - Password: testPassword
// @Tags auth
// @Accept json
// @Produce json
// @Param body body LoginRequest true "User credentials"
// @Success 200 {object} AuthResponse
// @Failure 401 {object} map[string]string
// @Router /login [post]
//
// This endpoint validates user credentials against the database
// and returns the authenticated user if credentials are valid.
//
// Parameters:
//   - c: Gin context for request/response handling
//
// =====================================================
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error("failed to parse login request: " + err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	user, err := h.service.Login(req.Email, req.Password)
	if err != nil {
		if err == ErrInvalidCredentials {
			log.Error("login failed: invalid credentials for " + req.Email)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}

		log.Error("login failed for " + req.Email + ": " + err.Error())
		c.JSON(http.StatusUnauthorized, gin.H{"error": "login failed"})
		return
	}

	// Generate JWT token
	token, err := auth.GenerateToken(user.ID, h.jwtSecret)
	if err != nil {
		log.Error("failed to generate token for " + req.Email + ": " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token generation failed"})
		return
	}

	response := AuthResponse{
		User:  user.ToUserResponse(),
		Token: token,
	}

	c.JSON(http.StatusOK, response)
}

// =====================================================
// Me returns the authenticated user's data.
//
// @Summary Get authenticated user
// @Description Retrieve the current authenticated user's profile
// @Tags auth
// @Produce json
// @Security BearerAuth
// @Success 200 {object} UserResponse
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /me [get]
//
// This endpoint returns the profile of the currently authenticated user
// based on the JWT token. The user ID is extracted from the token claims.
//
// Parameters:
//   - c: Gin context for request/response handling
//
// =====================================================
func (h *Handler) Me(c *gin.Context) {
	// Extract user_id from context (set by JWT middleware)
	userIDInterface, exists := c.Get("user_id")
	if !exists {
		log.Error("user_id not found in context")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Type assert to uint
	userID, ok := userIDInterface.(uint)
	if !ok {
		log.Error("invalid user_id type in context")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Fetch user from database
	user, err := h.service.GetByID(userID)
	if err != nil {
		if err == ErrInvalidCredentials {
			log.Error("user not found: " + fmt.Sprint(userID))
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}

		log.Error("failed to fetch user: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	response := user.ToUserResponse()
	c.JSON(http.StatusOK, response)
}
