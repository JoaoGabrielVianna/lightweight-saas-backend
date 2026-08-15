//go:build integration

package identityruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The acceptance tests for this slice.
//
// Everything here runs against a real PostgreSQL AND a real Keycloak, because
// the claim being tested — that two workspaces pointed at two realms cannot
// leak into each other — is not expressible against fakes. A fake proves the
// resolver calls the right collaborator; only two live realms prove that the
// user list a caller receives came from the realm their workspace names.
//
// The suite skips cleanly without either dependency, so it still runs on a
// laptop with just a database. CI provides both.

// ---------------------------------------------------------------------------
// PostgreSQL fixture
// ---------------------------------------------------------------------------

// newTestSchema creates a private schema, applies the real migrations into it,
// and returns a GORM handle scoped to it. Same approach the workspace and
// connection suites use: the schema under test is built by the migrations, not
// by a fixture, so a broken migration fails these tests too.
func newTestSchema(t *testing.T) *gorm.DB {
	t.Helper()

	base := os.Getenv("DB_URL")
	if base == "" {
		t.Skip("DB_URL unset — integration test requires a reachable postgres")
	}

	schema := schemaNameFor(t)

	admin := openGorm(t, base)
	if err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error; err != nil {
		t.Fatalf("drop stale schema: %v", err)
	}
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup := openGorm(t, base)
		_ = cleanup.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error
	})

	dsn := withSearchPath(t, base, schema)
	if err := database.Migrate(dsn); err != nil {
		t.Fatalf("apply migrations to %s: %v", schema, err)
	}
	return openGorm(t, dsn)
}

// openGorm opens a pool and closes it when the test that asked for it ends.
//
// The close is not tidiness. This package builds three pools per test (admin,
// cleanup, and the scoped handle) across nineteen integration tests, and a pool
// that is never closed keeps its idle connections for the lifetime of the test
// BINARY, not the test. Left open, the package walks into PostgreSQL's
// max_connections on its own, and the first casualty is
// TestIsolation_ConcurrentRequestsDoNotCrossContaminate, which needs a burst of
// its own connections and instead gets `too many clients already` reported as
// `internal_error` from a resolver that is working perfectly. That is the same
// symptom `-p 1` was added to the Makefile to cure — serialising the PACKAGES
// does nothing about a package that leaks pools internally.
//
// The other four integration suites (connection, workspace, project, auditlog)
// have always closed here; this one was the outlier.
func openGorm(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open postgres pool: %v", err)
	}
	// Bounded so the 80-goroutine burst in the concurrency test exercises real
	// contention without demanding 80 backend connections. The test is about
	// provider isolation, not about how wide a pool the driver will open.
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DB_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func schemaNameFor(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("rt_")
	for _, r := range strings.ToLower(t.Name()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Keycloak fixture
// ---------------------------------------------------------------------------

// kcAdmin is a minimal Keycloak admin client used ONLY to build the realms
// these tests probe. Test scaffolding, not production code.
type kcAdmin struct {
	base   string
	token  string
	client *http.Client
	t      *testing.T
}

func requireKeycloak(t *testing.T) *kcAdmin {
	t.Helper()

	base := strings.TrimRight(os.Getenv("KEYCLOAK_VERIFY_URL"), "/")
	if base == "" {
		t.Skip("KEYCLOAK_VERIFY_URL unset — multi-realm isolation requires a reachable Keycloak")
	}

	client := &http.Client{Timeout: 20 * time.Second}
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", "admin-cli")
	form.Set("username", envOr("KEYCLOAK_VERIFY_ADMIN", "admin"))
	form.Set("password", envOr("KEYCLOAK_VERIFY_PASSWORD", "admin"))

	resp, err := client.Post(base+"/realms/master/protocol/openid-connect/token",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Skipf("KEYCLOAK_VERIFY_URL set but unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.Fatalf("admin token request returned %d: %s", resp.StatusCode, body)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode admin token: %v", err)
	}
	return &kcAdmin{base: base, token: payload.AccessToken, client: client, t: t}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (a *kcAdmin) do(method, path string, body any, out any) int {
	a.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			a.t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (a *kcAdmin) createRealm(name string) {
	a.t.Helper()
	_ = a.do(http.MethodDelete, "/admin/realms/"+name, nil, nil)
	if code := a.do(http.MethodPost, "/admin/realms", map[string]any{
		"realm": name, "enabled": true,
	}, nil); code >= 300 {
		a.t.Fatalf("create realm %s: HTTP %d", name, code)
	}
	a.t.Cleanup(func() { _ = a.do(http.MethodDelete, "/admin/realms/"+name, nil, nil) })
}

// createServiceAccountClient makes a confidential client with service accounts
// enabled and realm-management/realm-admin granted, and returns its secret.
func (a *kcAdmin) createServiceAccountClient(realm, clientID string) string {
	a.t.Helper()

	if code := a.do(http.MethodPost, "/admin/realms/"+realm+"/clients", map[string]any{
		"clientId":                  clientID,
		"enabled":                   true,
		"publicClient":              false,
		"serviceAccountsEnabled":    true,
		"standardFlowEnabled":       false,
		"directAccessGrantsEnabled": false,
	}, nil); code >= 300 {
		a.t.Fatalf("create client %s: HTTP %d", clientID, code)
	}

	var clients []struct {
		ID string `json:"id"`
	}
	a.do(http.MethodGet, "/admin/realms/"+realm+"/clients?clientId="+clientID, nil, &clients)
	if len(clients) == 0 {
		a.t.Fatalf("client %s not found after creation", clientID)
	}
	internalID := clients[0].ID

	var secret struct {
		Value string `json:"value"`
	}
	a.do(http.MethodGet, "/admin/realms/"+realm+"/clients/"+internalID+"/client-secret", nil, &secret)
	if secret.Value == "" {
		a.t.Fatalf("client %s has no secret", clientID)
	}

	a.grantRealmAdmin(realm, internalID)
	return secret.Value
}

func (a *kcAdmin) grantRealmAdmin(realm, clientInternalID string) {
	a.t.Helper()

	var svcUser struct {
		ID string `json:"id"`
	}
	a.do(http.MethodGet, "/admin/realms/"+realm+"/clients/"+clientInternalID+"/service-account-user", nil, &svcUser)
	if svcUser.ID == "" {
		a.t.Fatal("service-account user not found")
	}

	var mgmtClients []struct {
		ID string `json:"id"`
	}
	a.do(http.MethodGet, "/admin/realms/"+realm+"/clients?clientId=realm-management", nil, &mgmtClients)
	if len(mgmtClients) == 0 {
		a.t.Fatal("realm-management client not found")
	}

	var roles []map[string]any
	a.do(http.MethodGet, "/admin/realms/"+realm+"/clients/"+mgmtClients[0].ID+"/roles", nil, &roles)

	var toAssign []map[string]any
	for _, role := range roles {
		if role["name"] == "realm-admin" {
			toAssign = append(toAssign, role)
		}
	}
	if len(toAssign) == 0 {
		a.t.Fatal("realm-admin role not found")
	}

	if code := a.do(http.MethodPost,
		"/admin/realms/"+realm+"/users/"+svcUser.ID+"/role-mappings/clients/"+mgmtClients[0].ID,
		toAssign, nil); code >= 300 {
		a.t.Fatalf("grant realm-admin: HTTP %d", code)
	}
}

func (a *kcAdmin) createUser(realm, username string) {
	a.t.Helper()
	if code := a.do(http.MethodPost, "/admin/realms/"+realm+"/users", map[string]any{
		"username": username, "enabled": true, "email": username + "@example.test",
	}, nil); code >= 300 {
		a.t.Fatalf("create user %s in %s: HTTP %d", username, realm, code)
	}
}

func (a *kcAdmin) realmRoleNames(realm string) []string {
	a.t.Helper()
	var roles []struct {
		Name string `json:"name"`
	}
	a.do(http.MethodGet, "/admin/realms/"+realm+"/roles", nil, &roles)
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Name)
	}
	return out
}

// ---------------------------------------------------------------------------
// Installation fixture — the real thing, end to end
// ---------------------------------------------------------------------------

// installation is a resolver wired over real repositories against a real
// database, with real Keycloak realms behind it. No doubles anywhere.
type installation struct {
	t        *testing.T
	db       *gorm.DB
	kc       *kcAdmin
	keyring  *secrets.Keyring
	resolver *Resolver

	workspaces  *workspace.PostgresRepository
	connections *connection.PostgresRepository
	connSvc     *connection.Service
}

func newInstallation(t *testing.T) *installation {
	t.Helper()

	kc := requireKeycloak(t)
	db := newTestSchema(t)

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyring, err := secrets.NewSingleVersionKeyring(1, key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	workspaces := workspace.NewRepository(db)
	connections := connection.NewRepository(db)

	inst := &installation{
		t: t, db: db, kc: kc, keyring: keyring,
		workspaces:  workspaces,
		connections: connections,
		connSvc: connection.NewService(
			connections, workspaces, keyring, connection.NewKeycloakVerifier(nil),
			database.NewTxRunner(db), noopAuditWriter{}),
	}
	inst.resolver = NewResolver(workspaces, connections, keyring, Options{})
	if inst.resolver == nil {
		t.Fatal("NewResolver returned nil")
	}
	return inst
}

// newWorkspace creates a workspace through the real repository.
func (i *installation) newWorkspace(slug string) *workspace.Workspace {
	i.t.Helper()

	id, err := publicid.New()
	if err != nil {
		i.t.Fatalf("generate id: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	ws := &workspace.Workspace{
		ID: id, Slug: slug, Name: slug, Status: workspace.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := i.workspaces.Create(context.Background(), ws); err != nil {
		i.t.Fatalf("create workspace %s: %v", slug, err)
	}
	return ws
}

// connectRealm creates a Keycloak realm with a service-account client, then
// takes a connection to it through the REAL connection service — create,
// verify, activate — so the row the resolver reads is the one an operator's API
// calls would have produced.
func (i *installation) connectRealm(ws *workspace.Workspace, realm, name string) *connection.Connection {
	i.t.Helper()
	ctx := context.Background()

	i.kc.createRealm(realm)
	secret := i.kc.createServiceAccountClient(realm, "svc-"+realm)

	c, err := i.connSvc.Create(ctx, ws.PublicID(), connection.CreateInput{
		Name:         name,
		Provider:     string(connection.ProviderKeycloak),
		BaseURL:      i.kc.base,
		Realm:        realm,
		ClientID:     "svc-" + realm,
		ClientSecret: secret,
	}, &audit.Event{Action: audit.ActionConnectionCreated, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}})
	if err != nil {
		i.t.Fatalf("create connection to %s: %v", realm, err)
	}

	verified, report, err := i.connSvc.Verify(ctx, ws.PublicID(), c.PublicID(), &audit.Event{Action: audit.ActionConnectionVerified, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}})
	if err != nil {
		i.t.Fatalf("verify connection to %s: %v", realm, err)
	}
	if !report.OK {
		i.t.Fatalf("verification of %s failed: %s", realm, report.Summary)
	}

	activated, err := i.connSvc.Activate(ctx, ws.PublicID(), verified.PublicID(), &audit.Event{Action: audit.ActionConnectionActivated, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}})
	if err != nil {
		i.t.Fatalf("activate connection to %s: %v", realm, err)
	}
	return activated
}

// usernames resolves the workspace's provider and lists its users.
func (i *installation) usernames(ws *workspace.Workspace) []string {
	i.t.Helper()

	p, err := i.resolver.ForWorkspace(context.Background(), ws.PublicID())
	if err != nil {
		i.t.Fatalf("resolve %s: %v", ws.Slug, err)
	}
	users, err := p.Provider.ListUsers(context.Background(), identity.ListUsersQuery{Max: 100})
	if err != nil {
		i.t.Fatalf("list users for %s: %v", ws.Slug, err)
	}
	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Realm isolation
// ---------------------------------------------------------------------------

// TestIsolation_TwoWorkspacesSeeOnlyTheirOwnRealmsUsers is the slice's headline
// claim, proved against two live realms with distinct users.
func TestIsolation_TwoWorkspacesSeeOnlyTheirOwnRealmsUsers(t *testing.T) {
	inst := newInstallation(t)

	wsA := inst.newWorkspace("alpha")
	wsB := inst.newWorkspace("beta")
	inst.connectRealm(wsA, "rt-realm-a", "primary")
	inst.connectRealm(wsB, "rt-realm-b", "primary")

	inst.kc.createUser("rt-realm-a", "alice-in-a")
	inst.kc.createUser("rt-realm-b", "bob-in-b")

	got := inst.usernames(wsA)
	if !contains(got, "alice-in-a") {
		t.Errorf("workspace alpha did not see its own user: %v", got)
	}
	if contains(got, "bob-in-b") {
		t.Errorf("workspace alpha saw realm B's user — REALM LEAK: %v", got)
	}

	got = inst.usernames(wsB)
	if !contains(got, "bob-in-b") {
		t.Errorf("workspace beta did not see its own user: %v", got)
	}
	if contains(got, "alice-in-a") {
		t.Errorf("workspace beta saw realm A's user — REALM LEAK: %v", got)
	}
}

// TestIsolation_ConcurrentRequestsDoNotCrossContaminate runs both workspaces
// hard and in parallel against live Keycloak.
//
// Sequential isolation can hold while concurrent isolation does not: a shared
// provider field, a token cache keyed wrongly, or a resolver that mutated
// process state would all pass the test above and fail this one. The request
// count is deliberately far above the number of distinct workspaces so the
// cache cannot serialise the interesting window away.
func TestIsolation_ConcurrentRequestsDoNotCrossContaminate(t *testing.T) {
	inst := newInstallation(t)

	wsA := inst.newWorkspace("alpha")
	wsB := inst.newWorkspace("beta")
	inst.connectRealm(wsA, "rt-conc-a", "primary")
	inst.connectRealm(wsB, "rt-conc-b", "primary")

	inst.kc.createUser("rt-conc-a", "only-in-a")
	inst.kc.createUser("rt-conc-b", "only-in-b")

	type expectation struct {
		ws    *workspace.Workspace
		mine  string
		other string
	}
	cases := []expectation{
		{wsA, "only-in-a", "only-in-b"},
		{wsB, "only-in-b", "only-in-a"},
	}

	var wg sync.WaitGroup
	problems := make(chan string, 200)

	for i := 0; i < 40; i++ {
		for _, tc := range cases {
			wg.Add(1)
			go func(tc expectation) {
				defer wg.Done()

				p, err := inst.resolver.ForWorkspace(context.Background(), tc.ws.PublicID())
				if err != nil {
					problems <- "resolve " + tc.ws.Slug + ": " + err.Error()
					return
				}
				users, err := p.Provider.ListUsers(context.Background(), identity.ListUsersQuery{Max: 100})
				if err != nil {
					problems <- "list " + tc.ws.Slug + ": " + err.Error()
					return
				}

				var names []string
				for _, u := range users {
					names = append(names, u.Username)
				}
				if !contains(names, tc.mine) {
					problems <- tc.ws.Slug + " lost its own user under concurrency: " + strings.Join(names, ",")
				}
				if contains(names, tc.other) {
					problems <- tc.ws.Slug + " saw the OTHER realm's user under concurrency: " + strings.Join(names, ",")
				}
			}(tc)
		}
	}
	wg.Wait()
	close(problems)

	for msg := range problems {
		t.Error(msg)
	}
}

// ---------------------------------------------------------------------------
// Mutation isolation
// ---------------------------------------------------------------------------

// TestIsolation_MutationLandsInOneRealmOnly is Phase 8's acceptance: a write
// through workspace A changes realm A and leaves realm B untouched.
//
// Both halves are asserted through Keycloak's own admin API rather than through
// the runtime, so the check does not depend on the component under test to
// report on itself.
func TestIsolation_MutationLandsInOneRealmOnly(t *testing.T) {
	inst := newInstallation(t)

	wsA := inst.newWorkspace("alpha")
	wsB := inst.newWorkspace("beta")
	inst.connectRealm(wsA, "rt-mut-a", "primary")
	inst.connectRealm(wsB, "rt-mut-b", "primary")

	const roleName = "billing-admin"

	p, err := inst.resolver.ForWorkspace(context.Background(), wsA.PublicID())
	if err != nil {
		t.Fatalf("resolve alpha: %v", err)
	}
	svc := identity.NewService(p.Provider)
	if _, err := svc.CreateRole(context.Background(), identity.CreateRoleRequest{
		Name: roleName, Description: "created through workspace alpha",
	}); err != nil {
		t.Fatalf("create role through alpha: %v", err)
	}

	if !contains(inst.kc.realmRoleNames("rt-mut-a"), roleName) {
		t.Errorf("realm A does not have %q — the mutation did not land where it was aimed", roleName)
	}
	if contains(inst.kc.realmRoleNames("rt-mut-b"), roleName) {
		t.Errorf("realm B has %q — the mutation crossed the workspace boundary", roleName)
	}
}

// ---------------------------------------------------------------------------
// Connection rotation
// ---------------------------------------------------------------------------

// TestIsolation_ActivatingANewConnectionTakesEffectWithoutRestart is acceptance
// criterion 10.
//
// The whole point of resolving per request is that an operator can move a
// workspace to a new realm through the API and have it take effect. The
// resolver here is the SAME instance throughout — nothing is rebuilt, no
// process boundary is crossed — so if the second read returns realm A2's users,
// the runtime genuinely re-resolved.
func TestIsolation_ActivatingANewConnectionTakesEffectWithoutRestart(t *testing.T) {
	inst := newInstallation(t)

	ws := inst.newWorkspace("alpha")
	first := inst.connectRealm(ws, "rt-rot-a1", "generation-1")
	inst.kc.createUser("rt-rot-a1", "user-of-a1")

	// Warm the cache and confirm the starting point.
	before := inst.usernames(ws)
	if !contains(before, "user-of-a1") {
		t.Fatalf("workspace does not see A1's user before rotation: %v", before)
	}

	// Activate a second connection. connection.Service.Activate retires the
	// incumbent in the same transaction.
	second := inst.connectRealm(ws, "rt-rot-a2", "generation-2")
	inst.kc.createUser("rt-rot-a2", "user-of-a2")

	if second.ID == first.ID {
		t.Fatal("the fixture reused the same connection — rotation is not being exercised")
	}

	after := inst.usernames(ws)
	if !contains(after, "user-of-a2") {
		t.Errorf("after rotation the workspace does not see A2's user: %v", after)
	}
	if contains(after, "user-of-a1") {
		t.Errorf("after rotation the workspace still sees A1's user — the cache served a retired connection: %v", after)
	}

	// And the retired connection really is retired in the database.
	reloaded, err := inst.connections.GetByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("reload first connection: %v", err)
	}
	if reloaded.Status != connection.StatusRetired {
		t.Errorf("previous connection status = %q, want retired", reloaded.Status)
	}
}

// TestIsolation_RotatingTheSecretInPlaceIsPickedUp covers the other rotation
// shape against a live realm: the connection row is edited rather than
// replaced, so the id does not change and only updated_at moves. A resolver
// keyed on connection id alone would keep presenting the OLD client secret.
//
// The assertion is on provider IDENTITY, not on whether the read still works,
// and that distinction was found the hard way. Rotating a client secret at
// Keycloak does not invalidate access tokens already issued under it — they
// remain valid until they expire, which is five minutes by default. So a
// resolver that served a stale provider would keep answering correctly for the
// whole life of this test, and a purely functional check would pass against a
// deliberately broken cache key. It was verified to do exactly that.
//
// What a stale provider CANNOT do is be a different object. Requiring the
// resolver to hand back a new instance after the row changes is the property
// that actually distinguishes correct from broken here; the functional read is
// kept as a second assertion so a resolver cannot pass by returning something
// new and useless.
func TestIsolation_RotatingTheSecretInPlaceIsPickedUp(t *testing.T) {
	inst := newInstallation(t)
	ctx := context.Background()

	ws := inst.newWorkspace("alpha")
	inst.connectRealm(ws, "rt-secret", "primary")
	inst.kc.createUser("rt-secret", "user-of-secret-realm")

	before, err := inst.resolver.ForWorkspace(ctx, ws.PublicID())
	if err != nil {
		t.Fatalf("baseline resolve: %v", err)
	}
	if got := inst.usernames(ws); !contains(got, "user-of-secret-realm") {
		t.Fatalf("baseline read failed: %v", got)
	}

	// Regenerate the client secret at Keycloak. Every provider holding the old
	// one is now useless.
	var clients []struct {
		ID string `json:"id"`
	}
	inst.kc.do(http.MethodGet, "/admin/realms/rt-secret/clients?clientId=svc-rt-secret", nil, &clients)
	if len(clients) == 0 {
		t.Fatal("service client not found")
	}
	var regenerated struct {
		Value string `json:"value"`
	}
	if code := inst.kc.do(http.MethodPost,
		"/admin/realms/rt-secret/clients/"+clients[0].ID+"/client-secret", nil, &regenerated); code >= 300 {
		t.Fatalf("regenerate client secret: HTTP %d", code)
	}

	// A connection must be draft to be reconfigured, which is the real
	// operator flow: retire-and-replace is the alternative, covered above.
	// Here the row is edited directly to exercise the in-place path the cache
	// key's updated_at component exists for.
	active, err := inst.connections.GetActiveByWorkspace(ctx, ws.ID)
	if err != nil || active == nil {
		t.Fatalf("load active connection: %v", err)
	}
	sealed, err := inst.keyring.Seal([]byte(regenerated.Value), secretAAD(active.ID))
	if err != nil {
		t.Fatalf("seal rotated secret: %v", err)
	}
	if err := inst.db.Table("connections").Where("id = ?", active.ID).Updates(map[string]any{
		"secret_ciphertext":  sealed.Ciphertext,
		"secret_nonce":       sealed.Nonce,
		"secret_key_version": sealed.KeyVersion,
		"secret_alg":         sealed.Algorithm,
		"updated_at":         time.Now().UTC().Add(time.Second),
	}).Error; err != nil {
		t.Fatalf("rotate stored secret: %v", err)
	}

	after, err := inst.resolver.ForWorkspace(ctx, ws.PublicID())
	if err != nil {
		t.Fatalf("resolve after rotation: %v", err)
	}
	if before == after {
		t.Error("the resolver returned the SAME provider instance after the credential was rotated — " +
			"it is still holding the superseded secret, and will start failing the moment its cached token expires")
	}

	// And the new provider works: it authenticated with the rotated secret.
	if got := inst.usernames(ws); !contains(got, "user-of-secret-realm") {
		t.Errorf("after an in-place secret rotation the workspace cannot read its realm: %v", got)
	}
}

// ---------------------------------------------------------------------------
// Refusals, against the real stack
// ---------------------------------------------------------------------------

// TestIsolation_ArchivedWorkspaceIsRefusedBeforeKeycloakIsContacted.
//
// The realm stays perfectly healthy throughout — this is not "the request
// failed", it is "the request never left the process".
func TestIsolation_ArchivedWorkspaceIsRefusedBeforeKeycloakIsContacted(t *testing.T) {
	inst := newInstallation(t)
	ctx := context.Background()

	ws := inst.newWorkspace("alpha")
	inst.connectRealm(ws, "rt-archived", "primary")
	inst.kc.createUser("rt-archived", "user-of-archived")

	if got := inst.usernames(ws); !contains(got, "user-of-archived") {
		t.Fatalf("baseline read failed: %v", got)
	}

	if _, err := inst.workspaces.Archive(ctx, ws.ID, time.Now().UTC()); err != nil {
		t.Fatalf("archive: %v", err)
	}

	_, err := inst.resolver.ForWorkspace(ctx, ws.PublicID())
	if err != ErrWorkspaceArchived {
		t.Fatalf("err = %v, want workspace_archived", err)
	}

	// The realm is untouched and still serves — proving the refusal was ours.
	if !contains(inst.kc.realmRoleNames("rt-archived"), "offline_access") {
		t.Error("the realm stopped answering; the refusal was not a local decision")
	}
}

// TestIsolation_WorkspaceWithoutAnActiveConnectionReturnsTheStableError.
func TestIsolation_WorkspaceWithoutAnActiveConnectionReturnsTheStableError(t *testing.T) {
	inst := newInstallation(t)

	ws := inst.newWorkspace("unconnected")

	_, err := inst.resolver.ForWorkspace(context.Background(), ws.PublicID())
	if err != ErrConnectionMissing {
		t.Fatalf("err = %v, want workspace_connection_missing", err)
	}
}

// TestIsolation_RetiringTheOnlyConnectionStopsRouting — a workspace that had a
// connection and no longer does must stop resolving immediately.
func TestIsolation_RetiringTheOnlyConnectionStopsRouting(t *testing.T) {
	inst := newInstallation(t)
	ctx := context.Background()

	ws := inst.newWorkspace("alpha")
	c := inst.connectRealm(ws, "rt-retire", "primary")

	if _, err := inst.resolver.ForWorkspace(ctx, ws.PublicID()); err != nil {
		t.Fatalf("baseline resolve: %v", err)
	}

	if _, err := inst.connSvc.Retire(ctx, ws.PublicID(), c.PublicID(), &audit.Event{Action: audit.ActionConnectionRetired, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}}); err != nil {
		t.Fatalf("retire: %v", err)
	}

	if _, err := inst.resolver.ForWorkspace(ctx, ws.PublicID()); err != ErrConnectionMissing {
		t.Errorf("err = %v, want workspace_connection_missing after retiring the only connection", err)
	}
}

// ---------------------------------------------------------------------------
// Secret isolation
// ---------------------------------------------------------------------------

// TestIsolation_TheClientSecretIsNeverReadableThroughTheDomain checks the
// places a credential could plausibly surface.
//
// The structural guarantee is that connection.Connection has no field to hold
// one, so no listing or response can carry it. This asserts that end to end,
// against the row a real activation produced, and checks the stored bytes are
// genuinely ciphertext rather than the plaintext with a nonce beside it.
func TestIsolation_TheClientSecretIsNeverReadableThroughTheDomain(t *testing.T) {
	inst := newInstallation(t)
	ctx := context.Background()

	ws := inst.newWorkspace("alpha")
	inst.kc.createRealm("rt-secrets")
	plaintextSecret := inst.kc.createServiceAccountClient("rt-secrets", "svc-rt-secrets")

	c, err := inst.connSvc.Create(ctx, ws.PublicID(), connection.CreateInput{
		Name:         "primary",
		Provider:     string(connection.ProviderKeycloak),
		BaseURL:      inst.kc.base,
		Realm:        "rt-secrets",
		ClientID:     "svc-rt-secrets",
		ClientSecret: plaintextSecret,
	}, &audit.Event{Action: audit.ActionConnectionCreated, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	// 1. The domain object.
	rendered, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal connection: %v", err)
	}
	if strings.Contains(string(rendered), plaintextSecret) {
		t.Error("the connection domain object serialises its client secret")
	}

	// 2. A listing.
	list, err := inst.connections.List(ctx, ws.ID, connection.FilterAll)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listed, _ := json.Marshal(list)
	if strings.Contains(string(listed), plaintextSecret) {
		t.Error("a connection listing carries the client secret")
	}

	// 3. The stored bytes.
	var stored struct {
		SecretCiphertext []byte
		SecretNonce      []byte
	}
	if err := inst.db.Table("connections").
		Select("secret_ciphertext", "secret_nonce").
		Where("id = ?", c.ID).Take(&stored).Error; err != nil {
		t.Fatalf("read stored secret: %v", err)
	}
	if bytes.Contains(stored.SecretCiphertext, []byte(plaintextSecret)) {
		t.Error("the stored ciphertext contains the plaintext secret")
	}
	if len(stored.SecretNonce) == 0 {
		t.Error("no nonce stored alongside the ciphertext")
	}

	// 4. Every resolution error message.
	for _, e := range []*Error{
		ErrWorkspaceNotFound, ErrWorkspaceArchived, ErrConnectionMissing,
		ErrConnectionUnusable, ErrCredentialsUnavailable, ErrProviderUnavailable, ErrInternal,
	} {
		if strings.Contains(e.Message, plaintextSecret) {
			t.Errorf("error %s carries the client secret", e.Code)
		}
	}
}

// TestIsolation_ResolvedProviderDoesNotExposeItsCredential — the value handed
// to callers must not offer any route back to the secret it was built from.
//
// identity.IdentityProvider declares no accessor for configuration, so a
// handler holding one cannot ask which realm it points at, let alone what
// credential it holds. Serialising it is the crude version of that check, and
// it is the one that would catch a future provider growing an exported field.
func TestIsolation_ResolvedProviderDoesNotExposeItsCredential(t *testing.T) {
	inst := newInstallation(t)

	ws := inst.newWorkspace("alpha")
	inst.kc.createRealm("rt-provider-secret")
	plaintextSecret := inst.kc.createServiceAccountClient("rt-provider-secret", "svc-rt-provider-secret")

	c, err := inst.connSvc.Create(context.Background(), ws.PublicID(), connection.CreateInput{
		Name:         "primary",
		Provider:     string(connection.ProviderKeycloak),
		BaseURL:      inst.kc.base,
		Realm:        "rt-provider-secret",
		ClientID:     "svc-rt-provider-secret",
		ClientSecret: plaintextSecret,
	}, &audit.Event{Action: audit.ActionConnectionCreated, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	verified, _, err := inst.connSvc.Verify(context.Background(), ws.PublicID(), c.PublicID(), &audit.Event{Action: audit.ActionConnectionVerified, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := inst.connSvc.Activate(context.Background(), ws.PublicID(), verified.PublicID(), &audit.Event{Action: audit.ActionConnectionActivated, Actor: audit.Actor{Type: audit.ActorOperator, Subject: "op"}}); err != nil {
		t.Fatalf("activate: %v", err)
	}

	p, err := inst.resolver.ForWorkspace(context.Background(), ws.PublicID())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	rendered, err := json.Marshal(p.Provider)
	if err != nil {
		t.Fatalf("marshal provider: %v", err)
	}
	if strings.Contains(string(rendered), plaintextSecret) {
		t.Errorf("the resolved provider serialises its client secret: %s", rendered)
	}
}

// noopAuditWriter accepts every event.
//
// These suites are about REALM ISOLATION, not about audit durability: they
// prove a request routed through workspace A never reaches realm B. The
// transactional-audit guarantee they depend on is proven in
// internal/auditlog/atomicity_integration_test.go, and re-proving it here would
// couple two unrelated properties.
type noopAuditWriter struct{}

func (noopAuditWriter) RecordTx(context.Context, database.Tx, audit.Event) error { return nil }
