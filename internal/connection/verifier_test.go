package connection

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// keycloakStub serves the four endpoints the probe touches, each with a
// configurable status, so every branch of the report can be produced without a
// live Keycloak.
type keycloakStub struct {
	discoveryStatus int
	tokenStatus     int
	tokenBody       string
	realmStatus     int
	usersStatus     int

	// grantedRoles are the realm-management roles the minted access token
	// claims. nil selects a write-capable default ("realm-admin") so the many
	// tests that care only about reachability keep meaning "fully working
	// connection". Set it explicitly to exercise the write-capability probe.
	grantedRoles []string

	// omitRealmManagement mints a token with NO realm-management entry in
	// resource_access — a client whose scope excludes those roles. The probe
	// must report `unknown`, never `full`.
	omitRealmManagement bool

	// requests records method+path so a test can assert the probe is read-only.
	requests []string
}

// stubAccessToken mints an unsigned JWT whose payload carries the granted
// realm-management roles, in the shape Keycloak actually issues for a service
// account. The signature segment is a placeholder: the probe deliberately does
// not verify it (see inspectWriteGrant), and a test that signed it would be
// asserting a property the production code does not have.
func stubAccessToken(roles []string, omitRealmManagement bool) string {
	claims := map[string]any{}
	if !omitRealmManagement {
		claims["resource_access"] = map[string]any{
			"realm-management": map[string]any{"roles": roles},
		}
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	seg := base64.RawURLEncoding.EncodeToString(payload)
	return "eyJhbGciOiJSUzI1NiJ9." + seg + ".not-a-real-signature"
}

func (s *keycloakStub) start(t *testing.T) string {
	t.Helper()

	if s.discoveryStatus == 0 {
		s.discoveryStatus = http.StatusOK
	}
	if s.tokenStatus == 0 {
		s.tokenStatus = http.StatusOK
	}
	if s.tokenBody == "" {
		roles := s.grantedRoles
		if roles == nil {
			roles = []string{"realm-admin"}
		}
		body, err := json.Marshal(map[string]any{
			"access_token": stubAccessToken(roles, s.omitRealmManagement),
			"expires_in":   300,
		})
		if err != nil {
			t.Fatalf("marshal stub token body: %v", err)
		}
		s.tokenBody = string(body)
	}
	if s.realmStatus == 0 {
		s.realmStatus = http.StatusOK
	}
	if s.usersStatus == 0 {
		s.usersStatus = http.StatusOK
	}

	mux := http.NewServeMux()
	record := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s.requests = append(s.requests, r.Method+" "+r.URL.Path)
			next(w, r)
		}
	}

	mux.HandleFunc("/realms/saas/.well-known/openid-configuration", record(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(s.discoveryStatus)
		_, _ = w.Write([]byte(`{"issuer":"http://stub/realms/saas"}`))
	}))
	mux.HandleFunc("/realms/saas/protocol/openid-connect/token", record(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(s.tokenStatus)
		_, _ = w.Write([]byte(s.tokenBody))
	}))
	mux.HandleFunc("/admin/realms/saas/users", record(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(s.usersStatus)
		_, _ = w.Write([]byte(`[]`))
	}))
	mux.HandleFunc("/admin/realms/saas", record(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(s.realmStatus)
		_, _ = w.Write([]byte(`{"realm":"saas"}`))
	}))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func target(base string) VerifyTarget {
	return VerifyTarget{BaseURL: base, Realm: "saas", ClientID: "svc", ClientSecret: "s3cr3t"}
}

// checkByName finds a check in the report.
func checkByName(t *testing.T, report VerifyReport, name string) Check {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("report has no %q check: %+v", name, report.Checks)
	return Check{}
}

func TestVerifier_HappyPathReportsFullAccess(t *testing.T) {
	stub := &keycloakStub{}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if !report.OK {
		t.Fatalf("report not OK: %+v", report)
	}
	if report.AccessMode != AccessModeFull {
		t.Errorf("access mode = %q, want full", report.AccessMode)
	}
	if len(report.Checks) != 6 {
		t.Errorf("got %d checks, want 6: %+v", len(report.Checks), report.Checks)
	}
	for _, c := range report.Checks {
		if !c.OK {
			t.Errorf("check %q failed: %s", c.Name, c.Detail)
		}
		if c.Detail == "" {
			t.Errorf("check %q has no detail", c.Name)
		}
	}
	if report.CheckedAt.IsZero() {
		t.Error("checked_at is zero")
	}
	if report.Summary == "" {
		t.Error("summary is empty")
	}
}

// ─── TD-024: write capability is proven, never assumed ──────────────────────

// TestVerifier_ReadOnlyServiceAccountIsNotFull is the regression this whole
// change exists for. `view-users` grants both admin reads the probe performs,
// so the pre-Slice-6 verifier graded this account `full` and the API told the
// console writes were supported. They are not.
func TestVerifier_ReadOnlyServiceAccountIsNotFull(t *testing.T) {
	stub := &keycloakStub{grantedRoles: []string{"view-users", "view-realm"}}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if !report.OK {
		t.Fatalf("a read-only account still authenticates and reads; report must be OK: %+v", report)
	}
	if report.AccessMode == AccessModeFull {
		t.Fatal("read-only service account graded `full` — TD-024 has regressed")
	}
	if report.AccessMode != AccessModeReadOnly {
		t.Errorf("access mode = %q, want read_only", report.AccessMode)
	}
	if report.AccessMode.CanWrite() {
		t.Error("read_only must not permit writes")
	}
	// The reads themselves must still be reported as working — that is the
	// whole distinction from `limited`.
	if !checkByName(t, report, CheckUsersListing).OK {
		t.Error("a read-only account CAN list users; the read check must pass")
	}
	wc := checkByName(t, report, CheckWriteCapable)
	if wc.OK {
		t.Error("write_capable must be false")
	}
	if !strings.Contains(wc.Detail, "manage-users") {
		t.Errorf("detail should name the remedy, got %q", wc.Detail)
	}
}

// TestVerifier_ManageUsersProvesWriteCapability — the narrower grant, without
// the realm-admin composite, is still proof.
func TestVerifier_ManageUsersProvesWriteCapability(t *testing.T) {
	stub := &keycloakStub{grantedRoles: []string{"view-users", "manage-users"}}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if report.AccessMode != AccessModeFull {
		t.Errorf("access mode = %q, want full: %+v", report.AccessMode, report.Checks)
	}
	if !report.AccessMode.CanWrite() {
		t.Error("full must permit writes")
	}
}

// TestVerifier_ManageRealmAloneIsNotWriteCapability — manage-realm permits
// realm-role writes while every user mutation stays refused. Accepting it
// would reproduce the TD-024 over-claim one endpoint over.
func TestVerifier_ManageRealmAloneIsNotWriteCapability(t *testing.T) {
	stub := &keycloakStub{grantedRoles: []string{"view-users", "view-realm", "manage-realm"}}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if report.AccessMode == AccessModeFull {
		t.Error("manage-realm alone must not be graded `full` — it does not permit user writes")
	}
	if report.AccessMode != AccessModeReadOnly {
		t.Errorf("access mode = %q, want read_only", report.AccessMode)
	}
}

// TestVerifier_AbsentGrantEvidenceIsUnknownNotFull — a client whose scope
// omits its realm-management roles gives the probe nothing to read. The honest
// answer is `unknown`; the dangerous one is `full`.
func TestVerifier_AbsentGrantEvidenceIsUnknownNotFull(t *testing.T) {
	stub := &keycloakStub{omitRealmManagement: true}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if !report.OK {
		t.Fatalf("reads succeeded; report must be OK: %+v", report)
	}
	if report.AccessMode != AccessModeUnknown {
		t.Errorf("access mode = %q, want unknown", report.AccessMode)
	}
	// `unknown` still permits the attempt — refusing on absent evidence would
	// break installations whose provider simply does not publish its grants.
	if !report.AccessMode.CanWrite() {
		t.Error("unknown must still permit the write attempt; provider_forbidden is the authoritative answer")
	}
	if checkByName(t, report, CheckWriteCapable).OK {
		t.Error("write_capable must be false when nothing was proven")
	}
}

// TestVerifier_OpaqueTokenDegradesToUnknown — a provider that returns a token
// this build cannot parse must not be graded `full`.
func TestVerifier_OpaqueTokenDegradesToUnknown(t *testing.T) {
	stub := &keycloakStub{tokenBody: `{"access_token":"opaque-not-a-jwt","expires_in":300}`}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if report.AccessMode != AccessModeUnknown {
		t.Errorf("access mode = %q, want unknown for an unparseable token", report.AccessMode)
	}
}

// TestVerifier_WriteProbePerformsNoExtraRequest — the write verdict is read
// from a token already in hand. If this ever starts costing a round trip, the
// "zero-mutation, zero-cost" claim in the docs needs revisiting.
func TestVerifier_WriteProbePerformsNoExtraRequest(t *testing.T) {
	stub := &keycloakStub{}
	base := stub.start(t)

	NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if len(stub.requests) != 4 {
		t.Errorf("probe made %d requests, want 4 (discovery, token, realm, users): %v",
			len(stub.requests), stub.requests)
	}
	for _, r := range stub.requests {
		if !strings.HasPrefix(r, "GET ") && !strings.HasPrefix(r, "POST /realms/saas/protocol/openid-connect/token") {
			t.Errorf("unexpected non-read request during verification: %s", r)
		}
	}
}

func TestAccessMode_CanWrite(t *testing.T) {
	for mode, want := range map[AccessMode]bool{
		AccessModeFull:     true,
		AccessModeUnknown:  true,
		AccessModeReadOnly: false,
		AccessModeLimited:  false,
	} {
		if got := mode.CanWrite(); got != want {
			t.Errorf("AccessMode(%q).CanWrite() = %v, want %v", mode, got, want)
		}
	}
}

// TestVerifier_IsReadOnly is the promise the endpoint makes to an operator
// pressing Verify against production: it creates no test user and modifies
// nothing.
func TestVerifier_IsReadOnly(t *testing.T) {
	stub := &keycloakStub{}
	base := stub.start(t)

	NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	for _, req := range stub.requests {
		method, path, _ := strings.Cut(req, " ")
		// The token endpoint is a POST by protocol, and creates nothing.
		if strings.HasSuffix(path, "/protocol/openid-connect/token") {
			continue
		}
		if method != http.MethodGet {
			t.Errorf("probe issued a %s to %s — verification must be read-only", method, path)
		}
	}
	if len(stub.requests) == 0 {
		t.Fatal("the probe made no requests")
	}
}

func TestVerifier_UnreachableProvider(t *testing.T) {
	// TEST-NET-1 (RFC 5737) never routes.
	v := NewKeycloakVerifier(&http.Client{Timeout: time.Second})
	report := v.Verify(context.Background(), target("http://192.0.2.1:1"))

	if report.OK {
		t.Fatal("report should not be OK")
	}
	if report.AccessMode != AccessModeUnknown {
		t.Errorf("access mode = %q, want unknown", report.AccessMode)
	}
	reachable := checkByName(t, report, CheckReachable)
	if reachable.OK {
		t.Error("reachable check should have failed")
	}
	// Later checks must not be invented when the first one settles it.
	if len(report.Checks) != 1 {
		t.Errorf("got %d checks, want only the failed one: %+v", len(report.Checks), report.Checks)
	}
}

func TestVerifier_RealmNotFound(t *testing.T) {
	stub := &keycloakStub{discoveryStatus: http.StatusNotFound}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if report.OK {
		t.Fatal("report should not be OK")
	}
	if !checkByName(t, report, CheckReachable).OK {
		t.Error("the provider WAS reachable; only the realm was missing")
	}
	if checkByName(t, report, CheckRealmExists).OK {
		t.Error("realm_exists should have failed")
	}
	if !strings.Contains(report.Summary, "realm") {
		t.Errorf("summary = %q, want it to name the realm", report.Summary)
	}
}

func TestVerifier_DiscoveryServerError(t *testing.T) {
	stub := &keycloakStub{discoveryStatus: http.StatusInternalServerError}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))
	if report.OK {
		t.Fatal("report should not be OK")
	}
	if !strings.Contains(checkByName(t, report, CheckRealmExists).Detail, "500") {
		t.Errorf("detail should name the status: %+v", report.Checks)
	}
}

func TestVerifier_BadCredentials(t *testing.T) {
	stub := &keycloakStub{tokenStatus: http.StatusUnauthorized}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	if report.OK {
		t.Fatal("report should not be OK")
	}
	auth := checkByName(t, report, CheckClientAuth)
	if auth.OK {
		t.Error("client_authenticated should have failed")
	}
	if !strings.Contains(auth.Detail, "rejected") {
		t.Errorf("detail = %q, want it to say the credentials were rejected", auth.Detail)
	}
	// The secret must not appear anywhere in the report.
	assertNoSecretInReport(t, report, "s3cr3t")
}

// TestVerifier_ServiceAccountsDisabled covers the single most common
// misconfiguration, which Keycloak reports as a 400 rather than a 401.
func TestVerifier_ServiceAccountsDisabled(t *testing.T) {
	stub := &keycloakStub{tokenStatus: http.StatusBadRequest}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

	auth := checkByName(t, report, CheckClientAuth)
	if auth.OK {
		t.Error("client_authenticated should have failed")
	}
	if !strings.Contains(auth.Detail, "service account") {
		t.Errorf("detail = %q, want it to point at service accounts", auth.Detail)
	}
}

func TestVerifier_TokenResponseWithoutAccessToken(t *testing.T) {
	stub := &keycloakStub{tokenBody: `{"expires_in":300}`}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))
	if report.OK {
		t.Fatal("report should not be OK without an access token")
	}
}

// TestVerifier_LimitedAccess is the access_mode distinction: authentication
// works, so the connection is healthy, but the service account cannot read.
func TestVerifier_LimitedAccess(t *testing.T) {
	tests := map[string]*keycloakStub{
		"cannot list users":     {usersStatus: http.StatusForbidden},
		"cannot read the realm": {realmStatus: http.StatusForbidden},
		"can do neither":        {realmStatus: http.StatusForbidden, usersStatus: http.StatusForbidden},
	}

	for name, stub := range tests {
		t.Run(name, func(t *testing.T) {
			base := stub.start(t)
			report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))

			if !report.OK {
				t.Error("a connection that authenticates is healthy; privileges are a separate axis")
			}
			if report.AccessMode != AccessModeLimited {
				t.Errorf("access mode = %q, want limited", report.AccessMode)
			}
			if !strings.Contains(report.Summary, "privilege") {
				t.Errorf("summary = %q, want it to point at privileges", report.Summary)
			}
		})
	}
}

func TestVerifier_ForbiddenDetailNamesTheFix(t *testing.T) {
	stub := &keycloakStub{usersStatus: http.StatusForbidden}
	base := stub.start(t)

	report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))
	detail := checkByName(t, report, CheckUsersListing).Detail
	if !strings.Contains(detail, "realm-management") {
		t.Errorf("detail = %q, want it to name the roles to grant", detail)
	}
}

// TestVerifier_NeverLeaksTheSecret is the report's hard requirement: it is
// rendered to an operator and stored as health_message.
func TestVerifier_NeverLeaksTheSecret(t *testing.T) {
	stubs := []*keycloakStub{
		{},
		{tokenStatus: http.StatusUnauthorized},
		{usersStatus: http.StatusForbidden},
		{discoveryStatus: http.StatusNotFound},
	}
	for _, stub := range stubs {
		base := stub.start(t)
		report := NewKeycloakVerifier(nil).Verify(context.Background(), target(base))
		assertNoSecretInReport(t, report, "s3cr3t")
	}
}

func assertNoSecretInReport(t *testing.T, report VerifyReport, secret string) {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Errorf("the verify report contains the client secret: %s", encoded)
	}
	if strings.Contains(string(encoded), "stub-token") {
		t.Errorf("the verify report contains the access token: %s", encoded)
	}
}

// TestVerifier_MalformedBaseURL — the service rejects these before storing, so
// this covers the defence in depth rather than the normal path.
func TestVerifier_MalformedBaseURL(t *testing.T) {
	v := NewKeycloakVerifier(&http.Client{Timeout: time.Second})
	report := v.Verify(context.Background(), VerifyTarget{BaseURL: "ftp://nope", Realm: "saas"})

	if report.OK {
		t.Fatal("report should not be OK")
	}
	if !strings.Contains(checkByName(t, report, CheckReachable).Detail, "URL") {
		t.Errorf("detail should explain the URL is unusable: %+v", report.Checks)
	}
}

// TestVerifier_HonoursContextCancellation keeps a cancelled request from
// holding the probe open.
func TestVerifier_HonoursContextCancellation(t *testing.T) {
	stub := &keycloakStub{}
	base := stub.start(t)

	c, cancel := context.WithCancel(context.Background())
	cancel()

	report := NewKeycloakVerifier(nil).Verify(c, target(base))
	if report.OK {
		t.Error("a cancelled probe must not report success")
	}
}

func TestTransportReason(t *testing.T) {
	tests := map[string]string{
		"context deadline exceeded":                         "timed out",
		"dial tcp: lookup nope: no such host":               "host does not resolve",
		"dial tcp 127.0.0.1:1: connect: connection refused": "connection refused",
		`x509: certificate signed by unknown authority`:     "TLS handshake failed",
		`unsupported protocol scheme "ftp"`:                 "base_url is not a valid http or https URL",
		"something else entirely":                           "network error",
	}
	for msg, want := range tests {
		if got := transportReason(errStub(msg)); got != want {
			t.Errorf("transportReason(%q) = %q, want %q", msg, got, want)
		}
	}
	if got := transportReason(nil); got != "unknown error" {
		t.Errorf("transportReason(nil) = %q", got)
	}
}

func TestItoa(t *testing.T) {
	for n, want := range map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 404: "404", 503: "503"} {
		if got := itoa(n); got != want {
			t.Errorf("itoa(%d) = %q, want %q", n, got, want)
		}
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
