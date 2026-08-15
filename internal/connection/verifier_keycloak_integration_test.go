//go:build integration

package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests run the verification probe against a REAL Keycloak.
//
// The stubbed tests in verifier_test.go pin how the probe classifies each HTTP
// status; only this file proves those statuses are the ones Keycloak actually
// returns. The distinction matters most for the two cases the report is
// designed around: a service account without realm-management roles (403, not
// 401) and a client without service accounts enabled (400, not 401).
//
// Gated on KEYCLOAK_VERIFY_URL rather than DB_URL, and skipped without it: the
// CI integration job deliberately starts PostgreSQL only, so this suite runs
// where a Keycloak is available and skips cleanly where one is not.
//
// Run locally with:
//
//	docker run -d --name kc -e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
//	  -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin -p 58080:8080 \
//	  quay.io/keycloak/keycloak:26.0 start-dev
//	KEYCLOAK_VERIFY_URL=http://localhost:58080 go test -tags=integration ./internal/connection/

// kcAdmin is a minimal Keycloak admin client used ONLY to build the fixtures
// these tests probe. It is test scaffolding, not production code.
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
		t.Skip("KEYCLOAK_VERIFY_URL unset — live verification test requires a reachable Keycloak")
	}

	client := &http.Client{Timeout: 15 * time.Second}
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

// createRealm makes a disposable realm and schedules its removal.
func (a *kcAdmin) createRealm(name string) {
	a.t.Helper()

	_ = a.do(http.MethodDelete, "/admin/realms/"+name, nil, nil) // ignore "not there"
	if code := a.do(http.MethodPost, "/admin/realms", map[string]any{
		"realm": name, "enabled": true,
	}, nil); code >= 300 {
		a.t.Fatalf("create realm %s: HTTP %d", name, code)
	}
	a.t.Cleanup(func() { _ = a.do(http.MethodDelete, "/admin/realms/"+name, nil, nil) })
}

// createServiceAccountClient makes a confidential client with service accounts
// enabled and returns its secret. When grantAdmin is true the service-account
// user also receives the realm-management `realm-admin` role, which is exactly
// the difference between AccessModeFull and AccessModeLimited.
func (a *kcAdmin) createServiceAccountClient(realm, clientID string, grantAdmin bool) string {
	a.t.Helper()
	if grantAdmin {
		return a.createServiceAccountClientWithRoles(realm, clientID, []string{"realm-admin"})
	}
	return a.createServiceAccountClientWithRoles(realm, clientID, nil)
}

// createServiceAccountClientWithRoles is the general form: it grants exactly
// the named realm-management roles to the client's service-account user.
//
// Passing {"view-realm","view-users"} builds the fixture TD-024 is about — an
// administrative client that can read everything the probe reads and mutate
// nothing.
func (a *kcAdmin) createServiceAccountClientWithRoles(realm, clientID string, roleNames []string) string {
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

	if len(roleNames) > 0 {
		a.grantRealmManagementRoles(realm, internalID, roleNames)
	}
	return secret.Value
}

// grantRealmManagementRoles assigns the named realm-management client roles to
// a client's service-account user.
func (a *kcAdmin) grantRealmManagementRoles(realm, clientInternalID string, roleNames []string) {
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

	wanted := make(map[string]bool, len(roleNames))
	for _, n := range roleNames {
		wanted[n] = true
	}

	var toAssign []map[string]any
	for _, role := range roles {
		if name, _ := role["name"].(string); wanted[name] {
			toAssign = append(toAssign, role)
		}
	}
	if len(toAssign) != len(roleNames) {
		a.t.Fatalf("wanted realm-management roles %v, found %d of them", roleNames, len(toAssign))
	}

	if code := a.do(http.MethodPost,
		"/admin/realms/"+realm+"/users/"+svcUser.ID+"/role-mappings/clients/"+mgmtClients[0].ID,
		toAssign, nil); code >= 300 {
		a.t.Fatalf("grant %v: HTTP %d", roleNames, code)
	}
}

// ---------------------------------------------------------------------------

// TestLiveVerify_FullAccess is the end-to-end happy path against real Keycloak:
// a correctly configured service account with realm-management roles.
func TestLiveVerify_FullAccess(t *testing.T) {
	admin := requireKeycloak(t)
	realm := "probe-full"
	admin.createRealm(realm)
	secret := admin.createServiceAccountClient(realm, "probe-client", true)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), VerifyTarget{
		BaseURL: admin.base, Realm: realm, ClientID: "probe-client", ClientSecret: secret,
	})

	if !report.OK {
		t.Fatalf("report not OK against a correctly configured Keycloak: %s", describe(report))
	}
	if report.AccessMode != AccessModeFull {
		t.Errorf("access mode = %q, want full: %s", report.AccessMode, describe(report))
	}
	for _, c := range report.Checks {
		if !c.OK {
			t.Errorf("check %q failed: %s", c.Name, c.Detail)
		}
	}
	if len(report.Checks) != 6 {
		t.Errorf("got %d checks, want 6", len(report.Checks))
	}
	if !report.AccessMode.CanWrite() {
		t.Error("a realm-admin service account must be graded write-capable")
	}
	assertNoSecretInReport(t, report, secret)
}

// ─── TD-024 against a real provider ─────────────────────────────────────────

// TestLiveVerify_ReadOnlyAdminClient is the fixture the three-value model could
// not see: an administrative client granted `view-realm` and `view-users` and
// nothing else. It passes both admin reads, so the pre-Slice-6 probe graded it
// `full` and the API told the console that writes were supported.
//
// The assertion that matters is not the label — it is that the label agrees
// with what Keycloak will actually permit, which the next test proves directly.
func TestLiveVerify_ReadOnlyAdminClient(t *testing.T) {
	admin := requireKeycloak(t)
	realm := "probe-readonly-client"
	admin.createRealm(realm)
	secret := admin.createServiceAccountClientWithRoles(realm, "probe-client",
		[]string{"view-realm", "view-users"})

	report := NewKeycloakVerifier(nil).Verify(context.Background(), VerifyTarget{
		BaseURL: admin.base, Realm: realm, ClientID: "probe-client", ClientSecret: secret,
	})

	if !report.OK {
		t.Fatalf("a read-only admin client authenticates and reads; report must be OK: %s", describe(report))
	}
	if report.AccessMode == AccessModeFull {
		t.Fatalf("read-only admin client graded `full` against real Keycloak — TD-024 has regressed: %s", describe(report))
	}
	if report.AccessMode != AccessModeReadOnly {
		t.Errorf("access mode = %q, want read_only: %s", report.AccessMode, describe(report))
	}
	if report.AccessMode.CanWrite() {
		t.Error("read_only must not permit writes")
	}
	// Reads must still be reported as working — the whole distinction from
	// `limited`, which is what an under-privileged account gets.
	if !checkByName(t, report, CheckRealmRead).OK || !checkByName(t, report, CheckUsersListing).OK {
		t.Errorf("view-realm + view-users can read; both read checks must pass: %s", describe(report))
	}
	assertNoSecretInReport(t, report, secret)
}

// TestLiveVerify_AccessModeMatchesRealWriteOutcome closes the loop. It takes
// both fixtures, asks Keycloak to perform an actual write with each service
// account, and asserts the verdict predicted the outcome.
//
// This is the invariant in executable form: LIGHTWEIGHT must never report
// write capability it has only inferred from reads.
func TestLiveVerify_AccessModeMatchesRealWriteOutcome(t *testing.T) {
	admin := requireKeycloak(t)
	realm := "probe-write-outcome"
	admin.createRealm(realm)

	rwSecret := admin.createServiceAccountClientWithRoles(realm, "rw-client", []string{"realm-admin"})
	roSecret := admin.createServiceAccountClientWithRoles(realm, "ro-client", []string{"view-realm", "view-users"})

	verifier := NewKeycloakVerifier(nil)

	for _, tc := range []struct {
		name       string
		clientID   string
		secret     string
		wantMode   AccessMode
		wantStatus int // what Keycloak answers to a real user creation
	}{
		{"read-write", "rw-client", rwSecret, AccessModeFull, http.StatusCreated},
		{"read-only", "ro-client", roSecret, AccessModeReadOnly, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := verifier.Verify(context.Background(), VerifyTarget{
				BaseURL: admin.base, Realm: realm, ClientID: tc.clientID, ClientSecret: tc.secret,
			})
			if report.AccessMode != tc.wantMode {
				t.Fatalf("access mode = %q, want %q: %s", report.AccessMode, tc.wantMode, describe(report))
			}

			status := admin.attemptUserCreate(realm, tc.clientID, tc.secret, tc.name+"-probe-user")
			if status != tc.wantStatus {
				t.Fatalf("real user creation returned %d, want %d", status, tc.wantStatus)
			}

			// The verdict and the outcome must agree. A `true` here with a 403
			// there is the exact lie TD-024 recorded.
			writeSucceeded := status < 300
			if report.AccessMode.CanWrite() != writeSucceeded {
				t.Errorf("access mode %q says can_write=%v, but Keycloak answered %d",
					report.AccessMode, report.AccessMode.CanWrite(), status)
			}
		})
	}
}

// attemptUserCreate authenticates as the given service account and tries to
// create a user, returning the raw status. It is the ground truth the verify
// verdict is checked against — and it runs in a disposable realm that the
// createRealm cleanup deletes.
func (a *kcAdmin) attemptUserCreate(realm, clientID, secret, username string) int {
	a.t.Helper()

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)

	resp, err := a.client.Post(
		a.base+"/realms/"+realm+"/protocol/openid-connect/token",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		a.t.Fatalf("service-account token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		a.t.Fatalf("service-account token returned %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		a.t.Fatalf("decode service-account token: %v", err)
	}

	body, err := json.Marshal(map[string]any{"username": username, "enabled": true})
	if err != nil {
		a.t.Fatalf("marshal user: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, a.base+"/admin/realms/"+realm+"/users", bytes.NewReader(body))
	if err != nil {
		a.t.Fatalf("build create-user request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+payload.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	created, err := a.client.Do(req)
	if err != nil {
		a.t.Fatalf("create user: %v", err)
	}
	defer func() { _ = created.Body.Close() }()
	return created.StatusCode
}

// TestLiveVerify_LimitedAccess proves the access_mode distinction against real
// Keycloak: the service account authenticates, so the connection is healthy,
// but without realm-management roles it cannot read. Keycloak answers 403 here
// — the status the stub test assumes.
func TestLiveVerify_LimitedAccess(t *testing.T) {
	admin := requireKeycloak(t)
	realm := "probe-limited"
	admin.createRealm(realm)
	secret := admin.createServiceAccountClient(realm, "probe-client", false)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), VerifyTarget{
		BaseURL: admin.base, Realm: realm, ClientID: "probe-client", ClientSecret: secret,
	})

	if !report.OK {
		t.Fatalf("a connection that authenticates must be healthy: %s", describe(report))
	}
	if report.AccessMode != AccessModeLimited {
		t.Errorf("access mode = %q, want limited: %s", report.AccessMode, describe(report))
	}
	if !checkByName(t, report, CheckClientAuth).OK {
		t.Error("client authentication should have succeeded")
	}
	if checkByName(t, report, CheckUsersListing).OK {
		t.Error("user listing should have been refused without realm-management roles")
	}
	assertNoSecretInReport(t, report, secret)
}

// TestLiveVerify_WrongSecret covers the most common real failure.
func TestLiveVerify_WrongSecret(t *testing.T) {
	admin := requireKeycloak(t)
	realm := "probe-badsecret"
	admin.createRealm(realm)
	admin.createServiceAccountClient(realm, "probe-client", true)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), VerifyTarget{
		BaseURL: admin.base, Realm: realm, ClientID: "probe-client", ClientSecret: "definitely-wrong",
	})

	if report.OK {
		t.Fatalf("report OK with a wrong secret: %s", describe(report))
	}
	auth := checkByName(t, report, CheckClientAuth)
	if auth.OK {
		t.Error("client_authenticated should have failed")
	}
	// The realm was still reached and found — that is the whole value of a
	// per-check report over a single boolean.
	if !checkByName(t, report, CheckReachable).OK || !checkByName(t, report, CheckRealmExists).OK {
		t.Errorf("the realm was reachable; only the credentials were wrong: %s", describe(report))
	}
	assertNoSecretInReport(t, report, "definitely-wrong")
}

// TestLiveVerify_UnknownClient — an unknown client id, which Keycloak also
// answers with 401.
func TestLiveVerify_UnknownClient(t *testing.T) {
	admin := requireKeycloak(t)
	realm := "probe-noclient"
	admin.createRealm(realm)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), VerifyTarget{
		BaseURL: admin.base, Realm: realm, ClientID: "does-not-exist", ClientSecret: "whatever",
	})

	if report.OK {
		t.Fatalf("report OK for an unknown client: %s", describe(report))
	}
	if checkByName(t, report, CheckClientAuth).OK {
		t.Error("client_authenticated should have failed")
	}
}

// TestLiveVerify_UnknownRealm proves real Keycloak returns 404 on the discovery
// document for a realm that does not exist — the status the probe reads as
// "realm not found".
func TestLiveVerify_UnknownRealm(t *testing.T) {
	admin := requireKeycloak(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), VerifyTarget{
		BaseURL: admin.base, Realm: "no-such-realm-at-all", ClientID: "c", ClientSecret: "s",
	})

	if report.OK {
		t.Fatalf("report OK for a nonexistent realm: %s", describe(report))
	}
	if !checkByName(t, report, CheckReachable).OK {
		t.Error("the provider WAS reachable")
	}
	if checkByName(t, report, CheckRealmExists).OK {
		t.Error("realm_exists should have failed")
	}
}

// TestLiveVerify_IsReadOnly confirms against real Keycloak that a verification
// leaves no trace: same user count before and after.
func TestLiveVerify_IsReadOnly(t *testing.T) {
	admin := requireKeycloak(t)
	realm := "probe-readonly"
	admin.createRealm(realm)
	secret := admin.createServiceAccountClient(realm, "probe-client", true)

	countUsers := func() int {
		var users []map[string]any
		admin.do(http.MethodGet, "/admin/realms/"+realm+"/users", nil, &users)
		return len(users)
	}

	before := countUsers()
	for i := 0; i < 3; i++ {
		report := NewKeycloakVerifier(nil).Verify(context.Background(), VerifyTarget{
			BaseURL: admin.base, Realm: realm, ClientID: "probe-client", ClientSecret: secret,
		})
		if !report.OK {
			t.Fatalf("verification %d failed: %s", i, describe(report))
		}
	}
	if after := countUsers(); after != before {
		t.Errorf("user count moved from %d to %d — verification must create nothing", before, after)
	}
}

// describe renders a report for a failure message.
func describe(report VerifyReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ok=%v mode=%s summary=%q checks=[", report.OK, report.AccessMode, report.Summary)
	for i, c := range report.Checks {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s:%v(%s)", c.Name, c.OK, c.Detail)
	}
	b.WriteString("]")
	return b.String()
}
