package server

import (
	_ "github.com/JoaoGabrielVianna/lightweight-saas-backend/docs"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
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
// the RequireAuth middleware, AND into the DEV-ONLY playground/debug surface
// which uses it for /auth/debug introspection. The playground is mounted
// only when DEV_PLAYGROUND_ENABLED=true — see internal/server/playground.go.
func (s *Server) SetupRoutes(userHandler *user.Handler, provider auth.AuthProvider) {
	SetupRouter(s.router, userHandler, provider)
	s.router.GET("/health", healthHandler)
	s.router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	mountPlayground(s.router, s.cfg, provider)
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
