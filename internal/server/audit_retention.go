package server

import (
	"context"
	"strconv"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auditlog"
)

// Audit retention.
//
// # Why in-process, and why this shape
//
// Three options were available and the trade is about failure modes, not
// convenience:
//
//	cron / external job   correct, and requires the operator to set one up.
//	                      A retention policy that only works if someone read
//	                      the manual is a policy that silently does not work,
//	                      and the failure surfaces as a full disk.
//	sweep on every write  no scheduler, and turns every mutation into a
//	                      DELETE over a growing table. The cost lands on the
//	                      request path, which is where it must not be.
//	in-process ticker     what this is. Runs where the retention window is
//	                      configured, needs nothing installed, and costs one
//	                      goroutine.
//
// # Why once at startup AND then daily
//
// The startup sweep is what makes the window true for an installation that has
// been down: a process restarted after a week away has a week of expired events
// and would otherwise carry them until the first tick. The daily tick is what
// keeps it true afterwards.
//
// Daily, and not hourly, because retention is measured in days: sweeping more
// often deletes the same rows a few hours earlier and pays a DELETE for it.
//
// # What this deliberately is not
//
// Not distributed, not leader-elected, not coordinated. Two replicas would both
// sweep, and the second one deletes nothing because the first already did —
// `DELETE ... WHERE occurred_at < cutoff` is idempotent, so concurrent sweeps
// are wasteful rather than wrong. Adding coordination would mean a lock table
// for a query whose worst case is running twice.

// auditSweepInterval is how often expired events are removed.
const auditSweepInterval = 24 * time.Hour

// auditSweepTimeout bounds one sweep.
//
// A DELETE over a long-neglected table can be slow, and a sweep that runs
// forever holds a connection from the pool the request path shares. Five
// minutes is far more than the indexed delete needs and short enough that a
// pathological case releases the connection rather than starving the API.
const auditSweepTimeout = 5 * time.Minute

// auditSweepStartupDelay lets the process finish becoming ready before the
// first sweep.
//
// Boot is when the connection pool is coldest and migrations may just have run.
// A DELETE competing with that would make readiness slower for no reason —
// nothing about retention is urgent to the second.
const auditSweepStartupDelay = 30 * time.Second

// StartAuditRetention runs the sweep loop until the process exits.
//
// A no-op when the service is nil, which is the deployment with no database.
//
// The goroutine is not stopped on shutdown, deliberately: it holds no client
// connection and blocks nothing, and the drain in lifecycle.go closes the
// database pool after in-flight requests finish. A sweep caught by that gets a
// closed-pool error, logs it, and the process exits — which is the correct
// outcome and cheaper than threading a cancel through the composition root for
// a background task whose worst interruption is a DELETE that runs again
// tomorrow.
func StartAuditRetention(service *auditlog.Service, retention time.Duration) {
	if service == nil {
		return
	}
	if retention <= 0 {
		// Config validation refuses this, so reaching it means a caller built a
		// Config by hand. Refuse rather than sweep with a zero window, which
		// would delete everything.
		log.Warn("audit retention is not positive; the sweep will not run")
		return
	}

	log.Info("audit retention: " + retention.String() +
		", swept every " + auditSweepInterval.String())

	go func() {
		time.Sleep(auditSweepStartupDelay)
		sweepAudit(service, retention)

		ticker := time.NewTicker(auditSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			sweepAudit(service, retention)
		}
	}()
}

func sweepAudit(service *auditlog.Service, retention time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), auditSweepTimeout)
	defer cancel()

	deleted, err := service.Purge(ctx, retention)
	if err != nil {
		// Warn, not error: retention failing is not an incident. The data is
		// still there and the next sweep will try again. Escalating it would
		// train an operator to ignore audit-related alerts, which is the last
		// thing that should be noise.
		log.Warn("audit retention sweep failed: " + err.Error())
		return
	}
	if deleted > 0 {
		log.Info("audit retention: removed " + strconv.FormatInt(deleted, 10) +
			" event(s) older than " + retention.String())
	}
}
