// =====================================================
// Lightweight SaaS Backend API
//
// @title Lightweight SaaS Backend API
// @version 0.4.0
// @description SaaS backend with Keycloak-issued JWT auth.
// @description All protected endpoints require a Bearer token obtained from Keycloak.
// @host localhost:8080
// @basePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description OPERATOR authentication. Type "Bearer" followed by a Keycloak-issued access token. The token must carry the realm `admin` role, which is re-checked against the provider on every request.
// @securityDefinitions.apikey ProjectKeyAuth
// @in header
// @name Authorization
// @description PROJECT authentication (machine-to-machine). Type "Bearer" followed by a project credential, e.g. `Bearer lw_sk_<lookup>_<secret>`. The credential is bound to exactly one workspace and carries an explicit scope set; the scope each operation requires is stated in its description. Control-plane operations (workspaces, connections, projects) are operator-only and answer `operator_only` to any credential. Swagger 2.0 cannot express per-operation scopes for an apiKey scheme, so they are documented in prose rather than declared — see docs/PROJECTS.md.
// =====================================================
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auditlog"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth/keycloak"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/banner"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/server"
)

var log = logger.New("main")

// Build metadata, set at link time by the Dockerfile:
//
//	-ldflags "-X main.version=v0.4.0 -X main.commit=abc1234"
//
// Both default to "unknown" rather than being required, so `go build ./cmd/api`
// on a laptop still produces a working binary. The value is operational: the
// first question about a misbehaving deployment is which build it is running,
// and a container image tag answers "which image was pulled", not "which code
// is in it".
var (
	version = "unknown"
	commit  = "unknown"
)

func main() {
	// The container healthcheck runs THIS binary rather than curl or wget.
	//
	// Alpine happens to ship wget, so this is not strictly necessary today —
	// but a healthcheck that depends on a tool the base image happens to
	// include breaks silently the day the base image changes, and "silently"
	// means the container is reported healthy because the check itself failed
	// to run. The binary is already there, already knows which port it serves,
	// and already knows which path is readiness.
	if healthcheckRequested() {
		os.Exit(runHealthcheck())
	}

	banner.ShowAppBanner()
	log.Info("version=" + version + " commit=" + commit)

	cfg := config.LoadConfig()

	db := database.Connect(cfg.DBUrl, database.WithMigrations(cfg.DBMigrateOnBoot))

	provider := mustBuildAuthProvider(cfg)

	// Auth events fan out to the security log AND to the metrics registry.
	// Chained rather than replaced: the log is the security trail and the
	// counters are the operational signal, and neither substitutes for the
	// other.
	auth.SetEventHook(server.WireAuthMetrics(metrics.Default, authEventLogger))

	// Durable audit. Wired BEFORE the fan-out below, because the fan-out is
	// what makes emitted events reach it, and the workspace audit API reads
	// what it writes.
	workspaceAuditHandler, auditRecorder, auditService := server.SetupWorkspaceAudit(db)

	// Wire the audit subsystem to a fan-out recorder: a structured log line,
	// the durable PostgreSQL trail, and a bounded in-memory ring that feeds the
	// legacy /admin/audit-events view. Ring capacity is intentionally small —
	// it is a recency window for one process, not history. History is the
	// table.
	//
	// auditRecorder is nil when there is no database, and audit.Multi skips
	// nil entries.
	auditMemory := logging.WireDefaultWithMemory(500, nilSafeRecorder(auditRecorder))
	auditHandler := server.NewAuditHandler(auditMemory)

	// Retention. Started here rather than inside the audit package so the
	// goroutine's lifetime is visible at the composition root, alongside every
	// other long-lived thing this process owns.
	server.StartAuditRetention(auditService, cfg.AuditRetention())

	userHandler := server.SetupUser(db)

	// Workspace domain — local persistence only, no identity provider
	// involved, so it is wired whenever the database is reachable.
	workspaceHandler := server.SetupWorkspace(db, auditRecorder)

	// Connection domain. Returns nil when no secret keyring is configured,
	// which omits the connection routes; an error means a keyring IS set but
	// unusable, which is an operator mistake worth refusing to boot on.
	connectionHandler, err := server.SetupConnection(db, cfg, auditRecorder)
	if err != nil {
		log.Fatal("init connections: " + err.Error())
	}

	// Master-key coverage. Reports at boot, and every few minutes after, which
	// key versions the stored credentials need and whether this process holds
	// them — the number an operator watches a rotation finish by.
	//
	// Deliberately NOT a readiness input: a missing historical key affects the
	// connections sealed under it, and taking the whole instance out of
	// rotation for that would punish every other tenant. See keycensus.go.
	server.StartSecretKeyCensus(server.SetupSecretKeyCensus(db, cfg))

	// Project domain — backends that consume this API with a machine
	// credential instead of an operator token. Local persistence only: a
	// credential is hashed, never sealed, so this needs no master key and is
	// wired whenever the database is.
	projectHandler, projectAuth := server.SetupProject(db, auditRecorder)

	// Workspace-scoped identity runtime. Resolves a provider per request from
	// the calling workspace's active Connection, so two workspaces pointed at
	// two Keycloak realms serve two disjoint sets of users from the same
	// process. Gated on the same master key as connections — with no key there
	// are no connections to resolve.
	workspaceIdentityHandler, err := server.SetupWorkspaceIdentity(db, cfg)
	if err != nil {
		log.Fatal("init workspace identity: " + err.Error())
	}

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
	srv.SetupRoutes(server.RouterDeps{
		User:           userHandler,
		Identity:       identityHandler,
		Audit:          auditHandler,
		Provider:       provider,
		AdminChecker:   adminChecker,
		SMTP:           smtpHandler,
		EmailTemplates: emailTemplatesHandler,
		Workspace:      workspaceHandler,
		Connection:     connectionHandler,

		Project:     projectHandler,
		ProjectAuth: projectAuth,

		WorkspaceAudit:    workspaceAuditHandler,
		WorkspaceIdentity: workspaceIdentityHandler,

		RateLimits: server.RateLimitSettings{
			EdgeRPS:       cfg.RateLimitEdgeRPS,
			CredentialRPS: cfg.RateLimitCredentialRPS,
		},
	})
	// Start blocks until SIGINT or SIGTERM, then drains. It returns an error
	// only when the server could not serve at all — a bound port, usually —
	// which is worth a non-zero exit so a supervisor does not report a clean
	// stop.
	if err := srv.Start(cfg.Port); err != nil {
		log.Fatal("server: " + err.Error())
	}
}

// nilSafeRecorder converts a typed-nil *auditlog.Recorder into a genuinely nil
// interface.
//
// Without it, `audit.Multi{sink, (*auditlog.Recorder)(nil), mem}` holds a
// non-nil interface wrapping a nil pointer: the `r == nil` check inside Multi
// reads false and Record is called on a nil receiver. This is the same trap
// SetupIdentity documents for auth.AdminChecker, and it fires the same way —
// only in the deployment that omitted the dependency, which is the one least
// likely to be tested.
func nilSafeRecorder(r *auditlog.Recorder) audit.Recorder {
	if r == nil {
		return nil
	}
	return r
}

// healthcheckRequested reports whether this invocation is a probe rather than
// the server.
//
// A bare flag check rather than the flag package: the server takes no other
// arguments, and registering a FlagSet here would make an unrecognised argument
// abort the SERVER, which is a worse failure than ignoring it.
func healthcheckRequested() bool {
	for _, arg := range os.Args[1:] {
		if arg == "-healthcheck" || arg == "--healthcheck" {
			return true
		}
	}
	return false
}

// runHealthcheck probes this instance's readiness and returns a process exit
// code.
//
// READINESS, not liveness. A container healthcheck feeds `depends_on:
// service_healthy`, and what a dependent needs to know is "can it serve
// traffic" — liveness would report healthy while migrations were still running,
// and the dependent would start against a half-built schema.
//
// It reads PORT directly instead of going through LoadConfig, because
// LoadConfig validates the whole configuration and calls log.Fatal: a probe
// that exits non-zero for a reason unrelated to health would mark a running,
// healthy container as unhealthy.
func runHealthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health/ready")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: "+err.Error())
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The body names which check failed and carries nothing sensitive, so
		// it goes to stderr where `docker inspect` will show it.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Fprintf(os.Stderr, "healthcheck: %d %s\n", resp.StatusCode, body)
		return 1
	}
	return 0
}

// jwksAttempts and jwksBackoff bound the wait for an issuer that is still
// starting. Deliberately the same policy, and the same numbers, as
// database.connectAttempts.
//
// The reasoning there transfers whole: a dependency a few seconds behind on a
// host reboot is ordinary, and an orchestrator that starts containers
// concurrently is ordinary. The only reason Keycloak did not already have this
// is that `depends_on` appeared to cover it, and `depends_on` covers exactly
// one deployment.
//
// It covers that one badly, too. `docker compose --profile dev-idp up` starts a
// Keycloak that needs a minute or more to import a realm on first boot, so the
// API died and restarted eight times before converging. It reached `ready` on
// its own every time — after printing eight FATAL lines, which is how an
// operator learns that this product's startup errors are not worth reading.
//
// Bounded, so this stays fail-fast rather than becoming fail-never: a wrong URL
// or a wrong realm exhausts all ten just as quickly and exits with the reason.
// What it must not do is keep a process alive and silent in front of an issuer
// that will never answer — readiness would 503 forever and page nobody.
var (
	jwksAttempts = 10
	jwksBackoff  = 3 * time.Second
)

// mustBuildAuthProvider constructs the Keycloak provider, retrying a bounded
// number of times while the issuer looks like it is still coming up, and
// failing fast once that budget is spent — surfacing a Keycloak
// misconfiguration here is much better than serving 401s in production.
func mustBuildAuthProvider(cfg *config.Config) auth.AuthProvider {
	kcCfg := keycloak.Config{
		URL:              cfg.KeycloakURL,
		Realm:            cfg.KeycloakRealm,
		ClientID:         cfg.KeycloakClientID,
		ClientSecret:     cfg.KeycloakClientSecret,
		JWKSURL:          cfg.KeycloakJWKSURL,
		AllowedClientIDs: cfg.KeycloakAllowedClientIDs,
	}

	var lastErr error
	for attempt := 1; attempt <= jwksAttempts; attempt++ {
		p, err := keycloak.NewProvider(context.Background(), kcCfg, keycloak.JWKSOptions{})
		if err == nil {
			if attempt > 1 {
				log.Info(fmt.Sprintf("issuer reachable after %d attempts", attempt))
			}
			log.Info("auth provider ready (keycloak realm=" + cfg.KeycloakRealm + ")")
			return p
		}
		lastErr = err
		if attempt < jwksAttempts {
			log.Warn(fmt.Sprintf("issuer not reachable (attempt %d/%d), retrying in %s: %v",
				attempt, jwksAttempts, jwksBackoff, err))
			time.Sleep(jwksBackoff)
		}
	}
	// Named separately from the driver's message because the two causes need
	// different actions: an issuer that never came up is an infrastructure
	// problem, a realm that does not exist is a configuration one, and the
	// operator cannot tell them apart from "connection refused" alone.
	log.Fatal(fmt.Sprintf("init auth provider: issuer %s realm %s unreachable after %d attempts: %v",
		cfg.KeycloakURL, cfg.KeycloakRealm, jwksAttempts, lastErr))
	return nil
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
