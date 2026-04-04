// =====================================================
// Lightweight SaaS Backend API
//
// This is a lightweight SaaS backend API with user authentication.
//
// @title Lightweight SaaS Backend API
// @version 1.0
// @description A lightweight SaaS backend with authentication and user management
// @host localhost:8080
// @basePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and JWT token.
// =====================================================

package main

import (
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/banner"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/server"
)

var log = logger.New("main")

func main() {
	banner.ShowAppBanner()
	cfg := config.LoadConfig()

	db := database.Connect(cfg.DBUrl)

	// Setup application handlers with JWT secret
	userHandler := server.SetupUser(db, cfg.JWTSecret)

	srv := server.NewServer(db, cfg)
	srv.SetupRoutes(userHandler)

	srv.Start(cfg.Port)
}
