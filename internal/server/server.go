package server

import (
	_ "github.com/JoaoGabrielVianna/lightweight-saas-backend/docs"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"
)

var log = logger.New("server")

// =====================================================
// SetupUser initializes and returns the user handler.
//
// This function encapsulates the setup of user-related dependencies
// (repository, service, handler) to keep initialization logic organized
// and separate from the main function.
//
// Parameters:
//   - db: The database connection
//
// Returns:
//   - *user.Handler: The initialized user handler
//
// =====================================================
func SetupUser(db *gorm.DB) *user.Handler {
	repo := user.NewRepository(db)
	service := user.NewService(repo)
	handler := user.NewHandler(service)
	return handler
}

type Server struct {
	router *gin.Engine
	db     *gorm.DB
}

func NewServer(db *gorm.DB, cfg *config.Config) *Server {
	// Apply Gin configuration from config
	cfg.ApplyGinConfig()

	r := gin.Default()

	return &Server{
		router: r,
		db:     db,
	}
}

func (s *Server) SetupRoutes(userHandler *user.Handler) {
	// Setup all application routes
	SetupRouter(s.router, userHandler)

	// Health check endpoint
	s.router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})

	// Swagger documentation endpoint
	s.router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
}

func (s *Server) Start(port string) {
	log.Info("Server is running on port " + port)
	s.router.Run(":" + port)
}
