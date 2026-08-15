package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"
	"github.com/gin-gonic/gin"
)

// /metrics exposure, and what a request log may contain.
//
// Both are surfaces where a mistake is invisible until it matters: metrics
// scraped by anyone who can reach the port, and a log line that outlives every
// rotation of the credential it recorded.

func metricsRouter(t *testing.T, enabled bool, token string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mountMetrics(r, enabled, token)
	return r
}

func metricsRequest(r *gin.Engine, remoteAddr, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = remoteAddr
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestMetricsEndpoint_AbsentWhenDisabled — off by default, and off means the
// route does not exist. An installation that has not decided how to protect
// operational data exposes none of it.
func TestMetricsEndpoint_AbsentWhenDisabled(t *testing.T) {
	r := metricsRouter(t, false, "")

	if w := metricsRequest(r, "127.0.0.1:5000", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d with metrics disabled, want 404", w.Code)
	}
}

// TestMetricsEndpoint_LoopbackOnlyWithoutAToken is the single-VPS default:
// Prometheus or curl on the same host works, nothing over the network does.
func TestMetricsEndpoint_LoopbackOnlyWithoutAToken(t *testing.T) {
	r := metricsRouter(t, true, "")

	for _, local := range []string{"127.0.0.1:5000", "[::1]:5000"} {
		if w := metricsRequest(r, local, ""); w.Code != http.StatusOK {
			t.Errorf("status from %s = %d, want 200", local, w.Code)
		}
	}
	for _, remote := range []string{"203.0.113.5:5000", "10.0.0.7:5000"} {
		if w := metricsRequest(r, remote, ""); w.Code != http.StatusNotFound {
			t.Errorf("status from %s = %d, want 404 — metrics are readable off-host", remote, w.Code)
		}
	}
}

// TestMetricsEndpoint_ForwardedHeadersCannotForgeLocality.
//
// The rate limiter honours X-Forwarded-For, because behind a proxy that IS the
// client. Here the same header would let anyone claim to be local and read the
// metrics, so this check uses the transport address only. The divergence is
// deliberate and worth pinning.
func TestMetricsEndpoint_ForwardedHeadersCannotForgeLocality(t *testing.T) {
	r := metricsRouter(t, true, "")

	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "203.0.113.5:5000"
		req.Header.Set(header, "127.0.0.1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("a remote caller claiming %s: 127.0.0.1 got %d — the loopback check "+
				"trusts a header the caller controls", header, w.Code)
		}
	}
}

// TestMetricsEndpoint_TokenAllowsRemoteScraping — what a real Prometheus can
// send. An OIDC flow is not available to a scraper, which is why this is a
// static bearer token rather than the operator's auth.
func TestMetricsEndpoint_TokenAllowsRemoteScraping(t *testing.T) {
	const token = "scrape-token-value"
	r := metricsRouter(t, true, token)

	if w := metricsRequest(r, "203.0.113.5:5000", token); w.Code != http.StatusOK {
		t.Errorf("status with the correct token = %d, want 200", w.Code)
	}
	for _, wrong := range []string{"", "wrong", token + "x", strings.ToUpper(token)} {
		if w := metricsRequest(r, "203.0.113.5:5000", wrong); w.Code != http.StatusNotFound {
			t.Errorf("status with token %q = %d, want 404", wrong, w.Code)
		}
	}
}

// TestMetricsEndpoint_TokenAlsoAppliesToLoopback — once a token is configured
// it is the rule, including on the host itself. Otherwise anything that can run
// a process on the box bypasses the token, which makes it decorative.
func TestMetricsEndpoint_TokenAlsoAppliesToLoopback(t *testing.T) {
	r := metricsRouter(t, true, "scrape-token-value")

	if w := metricsRequest(r, "127.0.0.1:5000", ""); w.Code != http.StatusNotFound {
		t.Errorf("loopback without the token = %d, want 404", w.Code)
	}
}

// TestMetricsEndpoint_UnauthorizedAnswersNotFound — 404 rather than 401. An
// unauthorized caller should not learn the endpoint exists, and there is no
// flow for them to authenticate into. Same reasoning that omits /admin/*
// entirely when it is unconfigured.
func TestMetricsEndpoint_UnauthorizedAnswersNotFound(t *testing.T) {
	w := metricsRequest(metricsRouter(t, true, "tok"), "203.0.113.5:5000", "")

	if w.Code != http.StatusUnauthorized && w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Code == http.StatusUnauthorized {
		t.Error("an unauthorized scrape answered 401, which confirms the endpoint exists")
	}
	if strings.Contains(w.Body.String(), "metric") {
		t.Errorf("the refusal body mentions metrics: %s", w.Body.String())
	}
}

// TestMetricsEndpoint_ServesTheExpositionFormat.
func TestMetricsEndpoint_ServesTheExpositionFormat(t *testing.T) {
	metrics.Default.ObserveRequest("GET", "/v1/workspaces", 200, 0.01)
	r := metricsRouter(t, true, "")

	w := metricsRequest(r, "127.0.0.1:5000", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("metrics are cacheable; a cached scrape reports stale numbers as current")
	}
	if !strings.Contains(w.Body.String(), "lightweight_http_requests_total") {
		t.Errorf("body does not contain the request counter: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The access log
// ---------------------------------------------------------------------------

// logLineFor builds the access line for a request with a given principal, using
// the same function the middleware calls.
func logLineFor(t *testing.T, setup func(*gin.Context)) string {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws_x/users", nil)
	c.Request.Header.Set("Authorization", "Bearer lw_sk_zzzzsecretzzzzz_qqqqqqqq")
	if setup != nil {
		setup(c)
	}
	return accessLine(c, "/v1/workspaces/:workspace_id/users", 200, 12345000)
}

// TestAccessLog_CorrelatesAMachineRequest.
//
// A read emits no audit event — audit is mutations only — so this line is the
// only place a successful M2M read is attributable. Without it, "what has this
// credential been doing" has no answer.
func TestAccessLog_CorrelatesAMachineRequest(t *testing.T) {
	line := logLineFor(t, func(c *gin.Context) {
		auth.StorePrincipal(c, auth.NewProjectPrincipal(&auth.ProjectPrincipal{
			ProjectID:    "prj_7c9e6679-7425-40de-944b-e07fc1f90ae7",
			CredentialID: "key_9b2f4c1a-1111-4222-8333-444455556666",
			WorkspaceID:  "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		}))
	})

	for _, want := range []string{
		"principal=project",
		"project_id=prj_7c9e6679-7425-40de-944b-e07fc1f90ae7",
		"credential_id=key_9b2f4c1a-1111-4222-8333-444455556666",
		"workspace_id=ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"route=/v1/workspaces/:workspace_id/users",
		"status=200",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the access line is missing %q:\n%s", want, line)
		}
	}
}

// TestAccessLog_NeverContainsCredentialMaterial.
//
// The Authorization header is on the request the line is built from, so
// "nothing copied it" is a property worth pinning rather than assuming. A log
// line outlives the credential it recorded.
func TestAccessLog_NeverContainsCredentialMaterial(t *testing.T) {
	line := logLineFor(t, func(c *gin.Context) {
		auth.StorePrincipal(c, auth.NewProjectPrincipal(&auth.ProjectPrincipal{
			ProjectID:    "prj_1",
			CredentialID: "key_1",
			WorkspaceID:  "ws_1",
		}))
	})

	for _, forbidden := range []string{
		"lw_sk_", "zzzzsecretzzzzz", "Bearer", "Authorization", "authorization",
	} {
		if strings.Contains(line, forbidden) {
			t.Errorf("the access line contains %q:\n%s", forbidden, line)
		}
	}
}

// TestAccessLog_AnonymousAndOperatorRequests — the other two principal shapes,
// including the /admin/* path, which stores an Identity rather than a Principal.
func TestAccessLog_AnonymousAndOperatorRequests(t *testing.T) {
	t.Run("anonymous", func(t *testing.T) {
		if line := logLineFor(t, nil); !strings.Contains(line, "principal=anonymous") {
			t.Errorf("line = %q, want principal=anonymous", line)
		}
	})

	t.Run("operator through the principal", func(t *testing.T) {
		line := logLineFor(t, func(c *gin.Context) {
			auth.StorePrincipal(c, auth.NewOperatorPrincipal(&auth.Identity{Subject: "op-1"}))
		})
		if !strings.Contains(line, "principal=operator") || !strings.Contains(line, "sub=op-1") {
			t.Errorf("line = %q", line)
		}
	})

	t.Run("operator through the legacy identity", func(t *testing.T) {
		line := logLineFor(t, func(c *gin.Context) {
			auth.StoreIdentity(c, &auth.Identity{Subject: "legacy-op"})
		})
		if !strings.Contains(line, "principal=operator") || !strings.Contains(line, "sub=legacy-op") {
			t.Errorf("/admin/* requests are logged as anonymous: %q", line)
		}
	})
}

// TestObserveRequests_CountsRefusals — the middleware is mounted before every
// gate, so the requests an operator most needs counted (429, 401, 403) are the
// ones a later mount point would never see.
func TestObserveRequests_CountsRefusals(t *testing.T) {
	reg := metrics.NewRegistry()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ObserveRequests(reg))
	r.Use(func(c *gin.Context) { c.AbortWithStatus(http.StatusTooManyRequests) })
	r.GET("/v1/thing", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	rendered := reg.Render()
	if !strings.Contains(rendered, `route="/v1/thing",status="429"`) {
		t.Errorf("a request refused by an earlier middleware was not counted:\n%s", rendered)
	}
}

// TestWireAuthMetrics_ChainsRatherThanReplaces — the security log must survive
// the addition of counters.
func TestWireAuthMetrics_ChainsRatherThanReplaces(t *testing.T) {
	reg := metrics.NewRegistry()

	var seen []auth.AuthEvent
	hook := WireAuthMetrics(reg, func(e auth.AuthEvent) { seen = append(seen, e) })

	hook(auth.AuthEvent{Kind: auth.EventValidationFailed, Reason: "bad token"})
	hook(auth.AuthEvent{Kind: auth.EventForbidden, Subject: "prj_1"})
	hook(auth.AuthEvent{Kind: auth.EventForbidden, Subject: "keycloak-sub"})

	if len(seen) != 3 {
		t.Errorf("the downstream hook saw %d events, want 3 — the security log was replaced", len(seen))
	}

	rendered := reg.Render()
	if !strings.Contains(rendered, "lightweight_auth_failures_total") {
		t.Error("an authentication failure was not counted")
	}
	if !strings.Contains(rendered, `lightweight_authorization_denials_total{principal="project"} 1`) {
		t.Errorf("project denials were not counted separately:\n%s", rendered)
	}
	if !strings.Contains(rendered, `lightweight_authorization_denials_total{principal="operator"} 1`) {
		t.Errorf("operator denials were not counted separately:\n%s", rendered)
	}
}

// TestWireAuthMetrics_SubjectIsNeverALabel — a Keycloak sub or a project id as
// a label would be per-caller cardinality and a privacy leak in one.
func TestWireAuthMetrics_SubjectIsNeverALabel(t *testing.T) {
	reg := metrics.NewRegistry()
	hook := WireAuthMetrics(reg, nil)

	hook(auth.AuthEvent{Kind: auth.EventForbidden, Subject: "prj_7c9e6679-7425-40de"})
	hook(auth.AuthEvent{Kind: auth.EventValidationFailed, Reason: "token from user alice@example.com"})

	rendered := reg.Render()
	for _, forbidden := range []string{"7c9e6679", "alice@example.com", "alice"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the rendered metrics contain %q:\n%s", forbidden, rendered)
		}
	}
}
