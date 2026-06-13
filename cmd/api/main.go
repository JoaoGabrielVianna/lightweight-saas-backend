// =====================================================
// Lightweight SaaS Backend API
//
// @title Lightweight SaaS Backend API
// @version 1.0
// @description SaaS backend with Keycloak-issued JWT auth.
// @description All protected endpoints require a Bearer token obtained from Keycloak.
// @host localhost:8080
// @basePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a Keycloak-issued access token.
// =====================================================
package main

import (
	"context"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth/keycloak"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/banner"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/server"
)

var log = logger.New("main")

func main() {
	banner.ShowAppBanner()
	cfg := config.LoadConfig()

	db := database.Connect(cfg.DBUrl)

	provider := mustBuildAuthProvider(cfg)
	auth.SetEventHook(authEventLogger)

	// Wire the audit subsystem (internal/audit) to a fan-out recorder:
	// the structured-log sink stays the durable trail; a bounded in-memory
	// ring buffer feeds the admin console's Audit Logs view so operators
	// can answer "what just happened?" without grepping logs. Capacity is
	// intentionally small — the buffer is a recency window, not history.
	auditMemory := logging.WireDefaultWithMemory(500)
	auditHandler := server.NewAuditHandler(auditMemory)

	userHandler := server.SetupUser(db)

	// Identity-management routes (admin-gated). When the admin client isn't
	// configured this returns (nil, nil, nil, nil) and the router omits /admin/*
	// entirely. adminChecker is the GAP-1 live-admin authorization seam —
	// non-nil whenever identity is configured. Passing it to SetupRoutes
	// mounts RequireLiveAdmin on /admin/*.
	identityHandler, adminChecker, identityProvider, err := server.SetupIdentity(cfg)
	if err != nil {
		log.Fatal("init identity: " + err.Error())
	}
	smtpHandler := server.NewSMTPHandler(identityProvider)
	emailTemplatesHandler := server.NewEmailTemplatesHandler(identityProvider)

	srv := server.NewServer(db, cfg)
	srv.SetupRoutes(userHandler, identityHandler, auditHandler, provider, adminChecker, smtpHandler, emailTemplatesHandler)
	srv.Start(cfg.Port)
}

// mustBuildAuthProvider constructs the Keycloak provider and fails fast if
// JWKS can't be fetched at startup — surfacing a Keycloak misconfiguration
// here is much better than serving 401s in production.
func mustBuildAuthProvider(cfg *config.Config) auth.AuthProvider {
	p, err := keycloak.NewProvider(context.Background(), keycloak.Config{
		URL:              cfg.KeycloakURL,
		Realm:            cfg.KeycloakRealm,
		ClientID:         cfg.KeycloakClientID,
		ClientSecret:     cfg.KeycloakClientSecret,
		JWKSURL:          cfg.KeycloakJWKSURL,
		AllowedClientIDs: cfg.KeycloakAllowedClientIDs,
	}, keycloak.JWKSOptions{})
	if err != nil {
		log.Fatal("init auth provider: " + err.Error())
	}
	log.Info("auth provider ready (keycloak realm=" + cfg.KeycloakRealm + ")")
	return p
}

// authEventLogger is registered as the global auth event hook. Today it
// writes to the structured logger; tomorrow it can fan out to Prometheus
// or OpenTelemetry without touching middleware code.
var authLog = logger.New("auth")

func authEventLogger(e auth.AuthEvent) {
	switch e.Kind {
	case auth.EventTokenValidated:
		authLog.Info("ok kind=" + string(e.Kind) +
			" sub=" + e.Subject +
			" method=" + e.Method +
			" path=" + e.Path +
			" dur=" + e.Duration.String())
	default:
		authLog.Warn("denied kind=" + string(e.Kind) +
			" method=" + e.Method +
			" path=" + e.Path +
			" reason=" + e.Reason +
			" dur=" + e.Duration.String())
	}
}
