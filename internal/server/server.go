package server

import (
	"context"
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
// provider → service → handler). Returns (nil, nil, nil) when the admin
// client credentials aren't configured — the router uses that signal to
// OMIT the /admin/* routes entirely (404 vs 503 — caller can't tell the
// feature exists).
//
// The third return value is the live-admin cache backing the GAP-1
// remediation (see docs/SECURITY_REMEDIATION_GAP1.md). It is also wired
// back into the identity handler so role/user mutations invalidate it
// immediately; the cache TTL only bounds the out-of-band revocation
// window (changes made directly in Keycloak Admin UI).
//
// Returns a non-nil error only on misconfiguration that the operator should
// fix before serving traffic; today that's "client id set but secret empty"
// (or vice versa). Network failures don't surface here — the admin client
// is lazy, the first /users request triggers token acquisition.
func SetupIdentity(cfg *config.Config) (*identity.Handler, *auth.CachedAdminChecker, error) {
	idEmpty := cfg.KeycloakAdminClientID == ""
	secretEmpty := cfg.KeycloakAdminClientSecret == ""
	if idEmpty && secretEmpty {
		log.Warn("Identity management routes disabled: KEYCLOAK_ADMIN_CLIENT_ID and KEYCLOAK_ADMIN_CLIENT_SECRET are unset")
		return nil, nil, nil
	}
	if idEmpty != secretEmpty {
		return nil, nil, fmt.Errorf("identity: half-configured admin client (id_set=%v, secret_set=%v) — set both or neither", !idEmpty, !secretEmpty)
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
		return nil, nil, fmt.Errorf("identity provider init: %w", err)
	}

	service := identity.NewService(provider)
	handler := identity.NewHandler(service)

	// Live-admin authorization seam for GAP-1. The upstream lookup uses
	// IdentityProvider.ListUserRoles — same admin client, same realm. The
	// cache TTL bounds Keycloak load for the steady-state admin workflow;
	// the handler's mutation hooks invalidate immediately so the cache
	// only ever holds stale data for out-of-band changes (operator going
	// straight to the Keycloak Admin UI).
	checker := auth.NewCachedAdminChecker(adminCheckerFromProvider(provider), cfg.AdminLiveCheckTTL())
	handler.SetAdminInvalidator(checker)

	log.Info("identity management enabled (admin client=" + cfg.KeycloakAdminClientID + ", base=" + adminURL +
		", live-admin TTL=" + cfg.AdminLiveCheckTTL().String() + ")")
	return handler, checker, nil
}

// adminCheckerFromProvider adapts an identity.IdentityProvider into the
// auth.AdminChecker interface without introducing an auth→identity import
// cycle. The adapter lives in the server tier (composition root) — both
// packages already depend on, and only on, this layer.
func adminCheckerFromProvider(p identity.IdentityProvider) auth.AdminChecker {
	return auth.AdminCheckerFunc(func(ctx context.Context, subject string) (bool, error) {
		roles, err := p.ListUserRoles(ctx, subject)
		if err != nil {
			return false, err
		}
		for _, r := range roles {
			if r.Name == adminRoleName {
				return true, nil
			}
		}
		return false, nil
	})
}

// adminRoleName mirrors identity.adminRoleName. Duplicated as an unexported
// constant here rather than re-exported from identity — the canonical name
// is "admin" and a rename is a realm-config change that touches the
// realm-export JSON, not these constants.
const adminRoleName = "admin"

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
//
// adminChecker carries the GAP-1 live-admin-check seam. nil disables the
// live check (test paths / no-identity deployments); when non-nil it is
// mounted as a third middleware on /admin/* after RequireAuth and
// RequireRole("admin").
func (s *Server) SetupRoutes(userHandler *user.Handler, identityHandler *identity.Handler, auditHandler *AuditHandler, provider auth.AuthProvider, adminChecker auth.AdminChecker) {
	SetupRouter(s.router, userHandler, identityHandler, auditHandler, provider, adminChecker)
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
