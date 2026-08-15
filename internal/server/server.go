package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/JoaoGabrielVianna/lightweight-saas-backend/docs"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auditlog"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	identitykc "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity/keycloak"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identityruntime"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/project"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-contrib/cors"
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

// SetupWorkspace composes the workspace domain wiring (repo → service →
// handler). Pure local persistence — no Keycloak, no admin client, so it is
// always available whenever the database is.
func SetupWorkspace(db *gorm.DB, auditRecorder *auditlog.Recorder) *workspace.Handler {
	// A typed nil in an interface is not nil, so the conversion happens HERE
	// rather than at the call site: passing a nil *auditlog.Recorder straight
	// through would hand the service a writer that panics on first mutation.
	// As an untyped nil it makes NewService return nil, which omits the routes.
	var auditWriter workspace.AuditWriter
	if auditRecorder != nil {
		auditWriter = auditRecorder
	}

	repo := workspace.NewRepository(db)
	// The runner is built from the SAME handle the repository uses. A runner
	// over a different pool would open a transaction the repository never
	// joins, and every atomicity test would pass while nothing was atomic.
	service := workspace.NewService(repo, database.NewTxRunner(db), auditWriter)
	return workspace.NewHandler(service)
}

// buildKeyring turns the normalised configuration into the keyring the runtime
// uses, or reports that no key is configured.
//
// Two call sites need the same keyring, and this is the only place either of
// them learns how the environment spells it. Everything about SECRETS_KEYRING,
// SECRETS_KEY_CURRENT and the legacy SECRETS_MASTER_KEY has already been
// resolved by config.Keyring(); what arrives here is a list of versions and the
// one that is current.
//
// Returns (nil, nil) when nothing is configured — a legitimate deployment
// state, not a failure.
func buildKeyring(cfg *config.Config) (*secrets.Keyring, error) {
	kc, err := cfg.Keyring()
	if err != nil {
		// config's errors name variables and version numbers, never material.
		return nil, err
	}
	if !kc.Configured() {
		return nil, nil
	}

	ring, err := secrets.NewKeyringFromBase64(kc.Keys, kc.Current)
	if err != nil {
		// The error from the secrets package never echoes the key material.
		return nil, fmt.Errorf("secrets keyring: %w", err)
	}
	return ring, nil
}

// describeKeyring renders a keyring for the boot log.
//
// Versions and nothing else. An operator reading a boot log needs to confirm
// that the process came up holding the keys they think it holds — which is a
// list of small integers, and is the ONLY part of a keyring that is safe to
// print.
func describeKeyring(ring *secrets.Keyring) string {
	versions := ring.Versions()
	parts := make([]string, 0, len(versions))
	for _, v := range versions {
		label := "v" + strconv.Itoa(v)
		if v == ring.CurrentVersion() {
			label += " (current)"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

// SetupConnection composes the connection domain wiring (repo → service →
// handler), including the secrets Keyring that seals provider credentials.
//
// Returns (nil, nil) when no key is configured: the connection routes are then
// omitted entirely, exactly as SetupIdentity omits /admin/* without admin
// credentials. That is deliberate rather than degraded — a connection exists to
// hold a provider credential, and there is no acceptable way to store one
// without a key.
//
// Returns an error when a keyring IS configured but unusable (wrong length, not
// base64, a current version that is not in the ring). That is an operator
// mistake worth failing the boot for: silently running without the connection
// API because a key was mistyped would be discovered much later, by someone
// wondering where the routes went.
func SetupConnection(db *gorm.DB, cfg *config.Config, auditRecorder *auditlog.Recorder) (*connection.Handler, error) {
	var auditWriter connection.AuditWriter
	if auditRecorder != nil {
		auditWriter = auditRecorder
	}

	keyring, err := buildKeyring(cfg)
	if err != nil {
		return nil, err
	}
	if keyring == nil {
		log.Warn("Connection routes disabled: no secret keyring configured " +
			"(set SECRETS_KEYRING=1:$(openssl rand -base64 32))")
		return nil, nil
	}

	repo := connection.NewRepository(db)
	workspaces := workspace.NewRepository(db)
	verifier := connection.NewKeycloakVerifier(nil)

	service := connection.NewService(repo, workspaces, keyring, verifier, database.NewTxRunner(db), auditWriter)
	log.Info("connection management enabled (provider credentials sealed with " +
		secrets.AlgorithmAESGCM + "; keyring " + describeKeyring(keyring) + ")")
	return connection.NewHandler(service), nil
}

// SetupSecretKeyCensus builds the rotator the key-version census reports from.
//
// Returns nil when no keyring is configured — no keyring means no sealed
// credentials, so there is nothing to take a census of. A configuration error
// is NOT returned here: SetupConnection has already refused the boot on one, so
// reaching this function means the keyring is buildable.
//
// It is a Rotator rather than a narrower reader because the census and the
// rotation ask the database the same question, and a second type that counted
// key versions its own way is a second place for "which rows still need the old
// key" to be computed differently from the command an operator trusts.
func SetupSecretKeyCensus(db *gorm.DB, cfg *config.Config) *connection.Rotator {
	keyring, err := buildKeyring(cfg)
	if err != nil || keyring == nil {
		return nil
	}
	return connection.NewRotator(db, keyring)
}

// SetupProject composes the project domain wiring (repo → service → handler)
// and the credential authenticator the /v1 middleware uses.
//
// Pure local persistence — no Keycloak, no admin client, no master key — so it
// is always available whenever the database is. That is deliberate and differs
// from SetupConnection: a project credential is hashed, never sealed, so it has
// no dependency on SECRETS_MASTER_KEY. An installation without a master key can
// still create projects and keys; those keys simply reach identity routes that
// answer workspace_connection_missing, which is the honest state of a workspace
// that routes nowhere.
//
// Returns both the handler and the authenticator because they are two faces of
// one domain: the handler is how an operator manages credentials, the
// authenticator is how a credential is accepted. Wiring one without the other
// would produce either keys nobody can use or authentication for keys nobody
// can create.
func SetupProject(db *gorm.DB, auditRecorder *auditlog.Recorder) (*project.Handler, auth.ProjectAuthenticator) {
	var auditWriter project.AuditWriter
	if auditRecorder != nil {
		auditWriter = auditRecorder
	}

	repo := project.NewRepository(db)
	workspaces := workspace.NewRepository(db)

	service := project.NewService(repo, workspaces, database.NewTxRunner(db), auditWriter)
	authenticator := project.NewAuthenticator(repo)

	log.Info("project credentials enabled (opaque keys, SHA-256 digests, no plaintext at rest)")
	return project.NewHandler(service), authenticator
}

// SetupWorkspaceIdentity composes the workspace-scoped identity runtime
// (workspace repo + connection repo + secrets Keyring → resolver → handler).
//
// Returns (nil, nil) when no key is configured, for the same reason
// SetupConnection does: with no key there are no connections, and with no
// connections there is nothing for a workspace to route through. The routes are
// then omitted entirely rather than mounted to answer 503.
//
// Returns an error when a keyring IS configured but unusable — an operator
// mistake worth refusing to boot on.
//
// Note what this function does NOT touch: cfg.Keycloak*. The workspace-scoped
// runtime takes its provider coordinates exclusively from persisted
// Connections, and legacy /admin/* takes its exclusively from the environment.
// The two authorities are disjoint by construction, which is what lets both
// exist without a precedence rule to get wrong. See
// docs/WORKSPACE_IDENTITY_RUNTIME.md §6.
func SetupWorkspaceIdentity(db *gorm.DB, cfg *config.Config) (*identityruntime.Handler, error) {
	keyring, err := buildKeyring(cfg)
	if err != nil {
		return nil, err
	}
	if keyring == nil {
		log.Warn("Workspace-scoped identity routes disabled: no secret keyring configured")
		return nil, nil
	}

	resolver := identityruntime.NewResolver(
		workspace.NewRepository(db),
		connection.NewRepository(db),
		keyring,
		identityruntime.Options{},
	)

	log.Info("workspace-scoped identity enabled (providers resolved per request from each workspace's active connection)")
	return identityruntime.NewHandler(resolver), nil
}

// SetupWorkspaceAudit composes the durable audit trail: the store, the recorder
// that persists emitted events, and the handler that reads them back.
//
// Wired whenever the database is. Unlike connections it needs no master key —
// an audit event holds no sealed value — and unlike /admin/* it needs no
// provider. That matters: the trail must exist in every deployment that can
// mutate anything, and a surface an installation can accidentally run without
// would be a trail nobody could rely on.
//
// Returns the recorder separately from the handler because they are wired into
// different places: the recorder joins the audit fan-out at process start, the
// handler mounts a route. Returning one combined value would force the caller
// to reach into it for both.
func SetupWorkspaceAudit(db *gorm.DB) (*auditlog.Handler, *auditlog.Recorder, *auditlog.Service) {
	store := auditlog.NewRepository(db)
	if store == nil {
		log.Warn("Durable audit disabled: no database handle")
		return nil, nil, nil
	}

	service := auditlog.NewService(store)
	log.Info("durable audit enabled (workspace-scoped history in PostgreSQL)")
	return auditlog.NewHandler(service), auditlog.NewRecorder(store), service
}

// SetupIdentity composes the identity-management wiring (Keycloak admin
// provider → service → handler). Returns (nil, nil, nil, nil) when the admin
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
// The fourth return value is the raw keycloak provider, exposed so the
// composition root can wire additional handlers (e.g. SMTPHandler) that
// need direct admin API access without going through identity.Service.
//
// Returns a non-nil error only on misconfiguration that the operator should
// fix before serving traffic; today that's "client id set but secret empty"
// (or vice versa). Network failures don't surface here — the admin client
// is lazy, the first /users request triggers token acquisition.
//
// The checker is returned as the auth.AdminChecker INTERFACE rather than as
// *auth.CachedAdminChecker, and that is load-bearing. Returning the concrete
// pointer meant the not-configured path handed back a typed nil, which a caller
// storing it in an interface field turns into a non-nil interface holding a nil
// pointer — so `checker != nil` reads true and RequireLiveAdmin gets mounted
// with a receiver that panics on first use. With the interface as the declared
// type, `return nil, nil, nil, nil` is a genuinely nil interface and the nil
// check downstream means what it says.
func SetupIdentity(cfg *config.Config) (*identity.Handler, auth.AdminChecker, *identitykc.Provider, error) {
	idEmpty := cfg.KeycloakAdminClientID == ""
	secretEmpty := cfg.KeycloakAdminClientSecret == ""
	if idEmpty && secretEmpty {
		log.Warn("Identity management routes disabled: KEYCLOAK_ADMIN_CLIENT_ID and KEYCLOAK_ADMIN_CLIENT_SECRET are unset")
		return nil, nil, nil, nil
	}
	if idEmpty != secretEmpty {
		return nil, nil, nil, fmt.Errorf("identity: half-configured admin client (id_set=%v, secret_set=%v) — set both or neither", !idEmpty, !secretEmpty)
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
		return nil, nil, nil, fmt.Errorf("identity provider init: %w", err)
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
	return handler, checker, provider, nil
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

// Server is the HTTP entry shell. It owns the Gin engine and the process
// lifecycle, and exposes SetupRoutes / Start to the main package.
type Server struct {
	router *gin.Engine
	db     *gorm.DB
	cfg    *config.Config

	// ready is the instance's serving state, shared by the readiness handler
	// and the shutdown sequence. See health.go.
	ready *readyState

	// mu guards the two fields below, which are written by the goroutine that
	// binds the listener and read by tests waiting for it.
	mu      sync.Mutex
	addr    string
	started chan struct{}
}

// NewServer builds the Gin engine with the project's Gin configuration.
func NewServer(db *gorm.DB, cfg *config.Config) *Server {
	cfg.ApplyGinConfig()

	// gin.New() + our own logger, NOT gin.Default().
	//
	// gin.Default() hard-wires gin.Logger(), whose formatter prints the request
	// URI including the raw query — so the admin console's PKCE callback,
	// `GET /admin?code=…&state=…`, wrote a live authorization code and a CSRF
	// state token into the access log on every operator login. Found by the
	// browser e2e suite's artifact scan; see internal/logging/access_log.go for
	// the full reasoning and the redaction rule. Recovery() is what
	// gin.Default() adds besides the logger, and it is kept.
	r := gin.New()
	r.Use(logging.AccessLogger(), gin.Recovery())

	// Metrics and the access log, before anything that can refuse a request —
	// a 429 from the edge limiter and a 401 from authentication are exactly the
	// requests an operator most needs counted, and a middleware mounted after
	// them would never see one.
	r.Use(ObserveRequests(metrics.Default))

	if len(cfg.CORSAllowedOrigins) > 0 {
		r.Use(cors.New(cors.Config{
			AllowOrigins:     cfg.CORSAllowedOrigins,
			AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
			ExposeHeaders:    []string{"Content-Length"},
			AllowCredentials: true,
			MaxAge:           12 * time.Hour,
		}))
	}

	return &Server{
		router:  r,
		db:      db,
		cfg:     cfg,
		ready:   newReadyState(db),
		started: make(chan struct{}),
	}
}

// Addr is the address the server actually bound, available once it is
// listening. Non-empty only after Started fires.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

func (s *Server) setAddr(addr string) {
	s.mu.Lock()
	s.addr = addr
	s.mu.Unlock()
}

// Started closes once the listener is bound and serving.
//
// It exists for tests, which otherwise have to poll a port and race the
// listener. Exported because the shutdown test lives in a different package
// than a table-driven unit test would.
func (s *Server) Started() <-chan struct{} { return s.started }

func (s *Server) markStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.started:
		// Already closed: serve() ran twice, which only a test does.
	default:
		close(s.started)
	}
}

// SetupRoutes mounts user routes plus operational endpoints (health, swagger).
//
// deps.Provider is threaded through to the router, which wires it into the
// RequireAuth middleware, AND into the DEV-ONLY playground/debug surface. Every
// other dependency is nilable with the meaning documented on RouterDeps.
func (s *Server) SetupRoutes(deps RouterDeps) {
	SetupRouter(s.router, deps)

	// Token introspection. Mounted here (not in SetupRouter) because the
	// handler needs *config.Config to report the expected issuer and the
	// allowed-client set — the two fields the admin console's Settings and
	// Overview views render.
	//
	// Authenticated: the response echoes the caller's own claims plus this
	// API's expected issuer/client whitelist. Requiring a valid token keeps
	// that config detail off the unauthenticated surface in production.
	//
	// The DEV-ONLY unauthenticated twin lives at /dev/auth/debug (see
	// mountPlayground) — it is the one that can explain WHY an invalid or
	// expired token was rejected, which this authenticated route cannot do
	// (RequireAuth rejects it first). Two paths, one handler, no collision.
	s.router.GET("/auth/debug", auth.RequireAuth(deps.Provider), authDebugHandler(s.cfg, deps.Provider))

	// Operational probes. Unauthenticated by necessity — a kubelet, a compose
	// healthcheck and a load balancer have no credentials — and safe to be so:
	// liveness reports one word, and readiness reports which named check
	// failed, never a hostname, a version or an error string.
	//
	// /health is the original path and keeps answering exactly as it did.
	s.router.GET("/health", healthHandler)
	s.router.GET("/health/live", livenessHandler)
	s.router.GET("/health/ready", readinessHandler(s.ready))

	mountMetrics(s.router, s.cfg.MetricsEnabled, s.cfg.MetricsToken)

	s.router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	mountLanding(s.router)
	mountPlayground(s.router, s.cfg, deps.Provider)
	mountAdminConsole(s.router, s.cfg)
}
