package server

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

var accessLog = logger.New("http")

// ObserveRequests records one metric sample and one log line per request.
//
// Both in one middleware because they measure the same event and must agree
// about what a "route" is: a metric bucketed by pattern and a log line carrying
// the concrete path is the correct split, and doing it twice invites the two to
// drift into bucketing differently.
//
// # What the log line carries, and what it does not
//
//	request_id  the handle a caller quotes and every /v1 error already returns
//	method      —
//	route       the PATTERN, so lines group the way the metrics do
//	status      —
//	dur         —
//	principal   operator | project | anonymous
//	project     prj_… and key_… and ws_…, for a machine caller only
//
// The project identifiers are here and nowhere else in the log. A read emits no
// audit event — audit is mutations only — so without this line a successful M2M
// read is correlatable by request id alone, and an operator asking "what has
// this credential been doing" has nothing to grep. Mutations still emit their
// audit event; this does not replace it and deliberately carries none of its
// detail.
//
// Never logged, at any level: the Authorization header, a credential in any
// form, a key hash, a connection secret, a provider client secret, a password.
// The principal fields above are PUBLIC ids — an operator sees them in the
// console and revokes by them — and the plaintext they authenticate with is
// never in the same object.
func ObserveRequests(reg *metrics.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		elapsed := time.Since(start)
		route := c.FullPath() // the registered pattern, "" when nothing matched
		status := c.Writer.Status()

		reg.ObserveRequest(c.Request.Method, route, status, elapsed.Seconds())

		if route == "" {
			route = "other"
		}
		accessLog.Info(accessLine(c, route, status, elapsed))
	}
}

// accessLine renders the log line.
//
// Assembled by hand rather than through a structured logger because the project
// has one logger and it takes a string; introducing a second logging API for
// one line would be a bigger change than the line is worth. The field=value
// shape is greppable and parses with logfmt.
func accessLine(c *gin.Context, route string, status int, elapsed time.Duration) string {
	line := "request_id=" + orDash(requestid.FromContext(c)) +
		" method=" + c.Request.Method +
		" route=" + route +
		" status=" + strconv.Itoa(status) +
		" dur=" + elapsed.Round(time.Microsecond).String()

	p, ok := auth.PrincipalFrom(c)
	switch {
	case ok && p.IsProject():
		line += " principal=project" +
			" project_id=" + p.Project.ProjectID +
			" credential_id=" + p.Project.CredentialID +
			" workspace_id=" + p.Project.WorkspaceID
	case ok && p.IsOperator():
		line += " principal=operator sub=" + p.Operator.Subject
	default:
		// Includes every request that failed authentication, and every
		// /admin/* request, which stores an Identity rather than a Principal.
		if id, found := auth.IdentityFrom(c); found && id != nil {
			line += " principal=operator sub=" + id.Subject
		} else {
			line += " principal=anonymous"
		}
	}
	return line
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// WireAuthMetrics subscribes the metrics registry to the existing auth event
// hook.
//
// Reusing that seam rather than instrumenting the middleware directly is what
// keeps internal/auth and internal/authz free of any metrics import: they
// already emit these events for the security log, and the numbers an operator
// wants — how many authentications are failing, how many identified callers are
// being refused — are exactly what is already being emitted.
//
// It CHAINS rather than replaces, so the existing log hook keeps running. A
// silent replacement would remove the security log to add a counter.
func WireAuthMetrics(reg *metrics.Registry, next func(auth.AuthEvent)) func(auth.AuthEvent) {
	return func(e auth.AuthEvent) {
		switch e.Kind {
		case auth.EventValidationFailed:
			reg.ObserveAuthFailure(string(e.Kind))
		case auth.EventForbidden:
			// Subject is a project id for a machine and a Keycloak sub for an
			// operator. Only the SHAPE is used, never the value: a subject as
			// a label would be per-caller cardinality.
			reg.ObserveAuthorizationDenial(principalShape(e.Subject))
		}
		if next != nil {
			next(e)
		}
	}
}

// principalShape maps a subject to a two-value label.
func principalShape(subject string) string {
	if len(subject) > 4 && subject[:4] == "prj_" {
		return "project"
	}
	return "operator"
}

// mountMetrics registers /metrics when it is switched on.
//
// # The exposure decision
//
// Metrics are operationally sensitive. The route table, traffic volumes, error
// rates and the number of workspaces in use are all inferable from a scrape,
// and none of it should be readable by anyone who can reach the port. So:
//
//	off by default            METRICS_ENABLED=false. An installation that has
//	                          not decided how to protect it exposes nothing.
//	no token → loopback only  the single-VPS case: Prometheus or curl on the
//	                          same host, nothing over the network.
//	token → bearer            METRICS_TOKEN, compared in constant time, which
//	                          is what a scraper can actually send.
//
// Deliberately NOT the operator's OIDC auth: a scraper cannot perform an
// authorization-code flow, and requiring one would push every installation to
// switch metrics off or front them with something worse.
//
// The loopback test is on the transport address and ignores X-Forwarded-For,
// unlike the rate limiter's clientIP. That difference is the point: a header a
// caller controls must not be able to make a remote request look local.
func mountMetrics(router *gin.Engine, enabled bool, token string) {
	if !enabled {
		return
	}

	router.GET("/metrics", func(c *gin.Context) {
		if !metricsAuthorized(c, token) {
			// 404, not 401. An unauthorized caller should not learn that the
			// endpoint exists, and there is no flow for them to authenticate
			// into — the same reasoning that omits /admin/* entirely when it
			// is unconfigured.
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8",
			[]byte(metrics.Default.Render()))
	})

	if token == "" {
		log.Info("metrics enabled at /metrics (loopback only — set METRICS_TOKEN to scrape remotely)")
	} else {
		log.Info("metrics enabled at /metrics (bearer token required)")
	}
}

func metricsAuthorized(c *gin.Context, token string) bool {
	if token == "" {
		return isLoopback(c.Request.RemoteAddr)
	}
	presented, _ := extractBearerToken(c.GetHeader("Authorization"))
	return constantTimeEqual(presented, token)
}

// isLoopback reports whether the TRANSPORT peer is on this machine.
//
// RemoteAddr only. A proxy header is caller-controlled and would let anyone
// claim to be local, which would turn the safest default into the least safe.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func extractBearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return "", false
	}
	return header[len(prefix):], true
}

// constantTimeEqual compares without leaking the answer through timing. A
// scrape token is a shared secret and a byte-by-byte comparison over repeated
// attempts is a guessing oracle.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
