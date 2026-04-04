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

	srv := server.NewServer(db, cfg)
	srv.SetupRoutes()

	srv.Start(cfg.Port)
}
