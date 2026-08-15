package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Liveness and readiness are different questions, and answering them with one
// endpoint gets both wrong.
//
//	liveness   is this process alive?          restart me if not
//	readiness  can this instance serve?        route traffic to me if so
//
// Conflating them produces two specific failures, and this codebase was exposed
// to both. A single `/health` that pings the database means a brief database
// blip gets the process KILLED and restarted, which cannot help — the new
// process will not find the database either, and now the restart loop is the
// outage. A single `/health` that only says "alive" means an orchestrator
// routes traffic to an instance whose migrations are still running.
//
// # What readiness must NOT depend on
//
// The health of any individual workspace's Keycloak.
//
// This is the load-bearing rule of a multi-tenant runtime and it is easy to get
// backwards. A workspace resolves to a Connection, which points at a realm; if
// readiness checked every connection, one tenant's Keycloak going down would
// take the whole instance out of the load balancer and every OTHER tenant with
// it. One broken provider must degrade exactly one workspace, and it already
// does: that request answers `provider_unavailable` (502), which is the correct
// blast radius.
//
// Connection health is a property of the Connection and belongs to that domain,
// where an operator can see and repair it. It is not a property of this
// instance. TestReadiness_IgnoresWorkspaceProviderHealth pins the distinction.

// readyState is the instance's serving state.
//
// The shutting-down flag is the reason this is a type rather than a closure:
// the shutdown path and the readiness handler both need it, they run on
// different goroutines, and the whole point is that the flag flips BEFORE the
// listener closes.
type readyState struct {
	// shuttingDown is set the moment a termination signal arrives, before the
	// HTTP server stops accepting. An atomic rather than a mutex because it is
	// read on every readiness probe and written exactly once.
	shuttingDown atomic.Bool

	// ping reports whether the database is reachable. nil means no database is
	// wired, which is a test fixture rather than a deployment and is reported
	// as such.
	//
	// A function rather than the *gorm.DB directly, so a test can drive the
	// lifecycle — where the interesting assertions are about ORDERING — without
	// standing up PostgreSQL. The production constructor below is the only
	// thing that builds the real one.
	ping func(context.Context) error

	// dbCheckTimeout bounds the readiness probe's database ping. A probe that
	// can hang is worse than one that fails: an orchestrator waiting on it
	// learns nothing and the instance stays in rotation.
	dbCheckTimeout time.Duration
}

func newReadyState(db *gorm.DB) *readyState {
	s := &readyState{dbCheckTimeout: 2 * time.Second}
	if db != nil {
		s.ping = func(ctx context.Context) error { return pingDB(ctx, db) }
	}
	return s
}

// beginShutdown marks the instance as not-ready. Called before the HTTP server
// stops accepting, so a load balancer sees 503 while requests are still being
// served — which is the entire point of a drain.
func (r *readyState) beginShutdown() { r.shuttingDown.Store(true) }

// isShuttingDown reports whether a drain has started.
func (r *readyState) isShuttingDown() bool { return r.shuttingDown.Load() }

// readinessReport is the response body.
//
// Per-check detail rather than a bare status, because the operator reading it
// is usually looking at a deployment that will not come up, and "not ready"
// with no reason sends them to the logs of a process whose logs they may not
// have. It carries no configuration, no hostnames and no error strings from the
// driver — just which named check failed.
type readinessReport struct {
	Status string            `json:"status" example:"ready"`
	Checks map[string]string `json:"checks"`
}

// check runs the readiness checks.
func (r *readyState) check(ctx context.Context) (bool, readinessReport) {
	report := readinessReport{Status: "ready", Checks: map[string]string{}}
	ok := true

	if r.isShuttingDown() {
		report.Checks["accepting"] = "draining"
		ok = false
	} else {
		report.Checks["accepting"] = "ok"
	}

	switch {
	case r.ping == nil:
		// A router built without a database is a test fixture, not a
		// deployment. Report it rather than dereferencing nil.
		report.Checks["database"] = "not configured"
		ok = false
	default:
		ctx, cancel := context.WithTimeout(ctx, r.dbCheckTimeout)
		defer cancel()

		if err := r.ping(ctx); err != nil {
			// The driver's error can name the host, the user and occasionally
			// the password. The operator gets the fact; the log gets the cause.
			report.Checks["database"] = "unreachable"
			log.Warn("readiness: database ping failed: " + err.Error())
			ok = false
		} else {
			report.Checks["database"] = "ok"
		}
	}

	if !ok {
		report.Status = "not ready"
	}
	return ok, report
}

// pingDB reaches the database through the pool the application uses, so the
// probe fails when the POOL is exhausted and not only when the server is down.
// A `SELECT 1` on a fresh connection would report healthy while every
// application request queued.
func pingDB(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// livenessHandler answers "is this process alive".
//
// It does no I/O, holds no lock and touches no dependency. That is the contract:
// the only correct remedy for a failed liveness probe is a restart, so anything
// a restart cannot fix must not be able to fail it. A database check here would
// turn a thirty-second database blip into a crash loop.
//
// @Summary     Liveness probe
// @Description Returns 200 while the process is running. No auth, no dependency checks, no I/O.
// @Description A failure here means the process is wedged and should be restarted.
// @Tags        operations
// @Produce     json
// @Success     200 {object} map[string]string
// @Router      /health/live [get]
func livenessHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "alive"})
}

// readinessHandler answers "can this instance serve traffic".
//
// 503 while draining or while a global dependency is unavailable, so an
// orchestrator takes the instance out of rotation without killing it.
//
// @Summary     Readiness probe
// @Description Returns 200 when this instance can serve traffic, 503 otherwise.
// @Description Checks only GLOBAL dependencies: the database, and whether a shutdown has begun.
// @Description A workspace whose Keycloak is down does NOT make the instance unready —
// @Description that request answers provider_unavailable and every other workspace keeps working.
// @Tags        operations
// @Produce     json
// @Success     200 {object} readinessReport
// @Failure     503 {object} readinessReport
// @Router      /health/ready [get]
func readinessHandler(state *readyState) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, report := state.check(c.Request.Context())
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		// Probes must never be served from a cache, by the client or by an
		// intermediary: a cached "ready" outlives the state it described.
		c.Header("Cache-Control", "no-store")
		c.JSON(status, report)
	}
}

// healthHandler is the ORIGINAL /health, kept exactly as it was.
//
// It predates the split and is wired into docker-compose files, uptime monitors
// and at least one runbook that this change cannot reach. Removing it to tidy
// the surface would break deployments to fix nothing; it is liveness, it was
// always liveness, and it costs one route.
//
// New deployments should use /health/live and /health/ready, which say which
// question they answer.
//
// @Summary     Liveness probe (legacy path)
// @Description Equivalent to /health/live. Kept for existing deployments and monitors.
// @Tags        operations
// @Produce     json
// @Success     200 {object} map[string]string
// @Router      /health [get]
func healthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
