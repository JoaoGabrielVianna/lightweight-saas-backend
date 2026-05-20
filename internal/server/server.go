package server

import (
	"fmt"

	_ "github.com/JoaoGabrielVianna/lightweight-saas-backend/docs"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	identitykc "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity/keycloak"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"
)

var log = logger.New("server")

// SetupUser composes the user domain wiring (repo → service → handler).
// No auth secrets here — token validation is the provider's job.
func SetupUser(db *gorm.DB) *user.Handler {
	repo := user.NewRepository(db)
	service := user.NewService(repo)
	return user.NewHandler(service)
}

// SetupIdentity composes the identity-management wiring (Keycloak admin
// provider → service → handler). Returns (nil, nil) when the admin client
// credentials aren't configured — the router uses that signal to OMIT the
// /users routes entirely (404 vs 503 — caller can't tell the feature exists).
//
// Returns a non-nil error only on misconfiguration that the operator should
// fix before serving traffic; today that's "client id set but secret empty"
// (or vice versa). Network failures don't surface here — the admin client
// is lazy, the first /users request triggers token acquisition.
func SetupIdentity(cfg *config.Config) (*identity.Handler, error) {
	idEmpty := cfg.KeycloakAdminClientID == ""
	secretEmpty := cfg.KeycloakAdminClientSecret == ""
	if idEmpty && secretEmpty {
		log.Warn("Identity management routes disabled: KEYCLOAK_ADMIN_CLIENT_ID and KEYCLOAK_ADMIN_CLIENT_SECRET are unset")
		return nil, nil
	}
	if idEmpty != secretEmpty {
		return nil, fmt.Errorf("identity: half-configured admin client (id_set=%v, secret_set=%v) — set both or neither", !idEmpty, !secretEmpty)
	}

	adminURL := cfg.KeycloakAdminBaseURL
	if adminURL == "" {
		adminURL = cfg.KeycloakURL
	}

	provider, err := identitykc.NewProvider(identitykc.AdminConfig{
		BaseURL:      adminURL,
		Realm:        cfg.KeycloakRealm,
		ClientID:     cfg.KeycloakAdminClientID,
		ClientSecret: cfg.KeycloakAdminClientSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("identity provider init: %w", err)
	}

	log.Info("identity management enabled (admin client=" + cfg.KeycloakAdminClientID + ", base=" + adminURL + ")")
	return identity.NewHandler(identity.NewService(provider)), nil
}

// Server is the HTTP entry shell. It owns the Gin engine and exposes
// SetupRoutes / Start to the main package.
type Server struct {
	router *gin.Engine
	db     *gorm.DB
	cfg    *config.Config
}

// NewServer builds the Gin engine with the project's Gin configuration.
func NewServer(db *gorm.DB, cfg *config.Config) *Server {
	cfg.ApplyGinConfig()
	return &Server{
		router: gin.Default(),
		db:     db,
		cfg:    cfg,
	}
}

// SetupRoutes mounts user routes plus operational endpoints (health, swagger).
// The auth provider is threaded through to the router which wires it into
// the RequireAuth middleware, AND into the DEV-ONLY playground/debug surface.
// The identity handler may be nil — the router gates /users routes on that.
func (s *Server) SetupRoutes(userHandler *user.Handler, identityHandler *identity.Handler, provider auth.AuthProvider) {
	SetupRouter(s.router, userHandler, identityHandler, provider)
	s.router.GET("/health", healthHandler)
	s.router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	mountPlayground(s.router, s.cfg, provider)
	mountAdminConsole(s.router, s.cfg)
}

// healthHandler is exported via Swagger so `/health` is discoverable in the
// generated API doc. Plain liveness check — no auth, no DB ping.
//
// @Summary     Liveness probe
// @Description Returns 200 if the process is up. No auth required, no dependency checks.
// @Tags        operations
// @Produce     json
// @Success     200 {object} map[string]string
// @Router      /health [get]
func healthHandler(c *gin.Context) {
	c.JSON(200, gin.H{"status": "ok"})
}

// Start blocks listening on the given port.
func (s *Server) Start(port string) {
	log.Info("Server is running on port " + port)
	if err := s.router.Run(":" + port); err != nil {
		log.Fatal("server stopped: " + err.Error())
	}
}
