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
	"net/http"

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
//
// =====================================================
type Handler struct {
	service *Service
}

// =====================================================
// NewHandler creates a new authentication handler.
//
// This constructor initializes a handler with the provided
// service instance for handling HTTP requests.
//
// Parameters:
//   - service: The user service instance
//
// Returns:
//   - A new Handler instance
//
// =====================================================
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// =====================================================
// Register creates a new user account.
//
// @Summary Register user
// @Description Create a new user account with email and password
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

	response := AuthResponse{
		User:  user.ToUserResponse(),
		Token: "",
	}

	c.JSON(http.StatusOK, response)
}
