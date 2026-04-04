package server

import (
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var log = logger.New("server")

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

func (s *Server) SetupRoutes() {
	s.router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})
}

func (s *Server) Start(port string) {
	log.Info("Server is running on port " + port)
	s.router.Run(":" + port)
}
