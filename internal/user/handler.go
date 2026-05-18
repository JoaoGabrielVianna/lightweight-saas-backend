// Package user — HTTP handlers.
//
// Register/Login are intentionally absent: Keycloak owns identity. Clients
// obtain tokens from Keycloak directly (Authorization Code + PKCE in
// browsers, or Direct Access Grants for CLIs/tests), then call protected
// endpoints with a Bearer token. The auth middleware validates the token
// and stores an *auth.Identity in the gin context for handlers to consume.
package user

import (
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/gin-gonic/gin"
)

var log = logger.New("user-handler")

// Handler is the user-domain HTTP surface.
type Handler struct {
	service *Service
}

// NewHandler constructs a Handler around the given Service. No auth
// configuration is needed — middleware handles all token concerns.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Me returns the authenticated user's local projection, JIT-provisioning
// the local row on first call for a given subject.
//
// @Summary     Get authenticated user
// @Description Returns the local user record for the authenticated subject.
// @Description On first call for a new subject, JIT-creates the local row
// @Description from token claims (sub, email, preferred_username).
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} UserResponse
// @Failure     401 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /me [get]
func (h *Handler) Me(c *gin.Context) {
	id, ok := auth.IdentityFrom(c)
	if !ok {
		log.Error("Me handler invoked without identity in context — middleware not wired?")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	user, err := h.service.EnsureUser(id)
	if err != nil {
		log.Error("EnsureUser failed for sub=" + id.Subject + ": " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, user.ToUserResponse())
}
