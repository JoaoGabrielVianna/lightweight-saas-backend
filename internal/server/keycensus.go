package server

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"
)

// The secret key census: which master-key versions the persisted credentials
// actually need, and whether this process holds them.
//
// # Why this is not a readiness check
//
// It would be easy to make /health/ready fail when a persisted key version is
// missing, and it would be wrong for the same reason health.go already refuses
// to check per-workspace Keycloak health: a missing HISTORICAL key affects the
// connections sealed under it and nothing else. One workspace's credential
// being unopenable must degrade one workspace — that request answers
// credentials_unavailable — and must not take the instance out of the load
// balancer, along with every other tenant on it, plus /v1/projects,
// /v1/workspaces and the audit API, none of which involve a key at all.
//
// So the model is (C) from the three available: start, serve, and fail only the
// affected workspaces. The condition is not hidden — it is the loudest thing in
// the boot log and it has a gauge — but it does not get to decide whether an
// instance serves traffic.
//
// The case that DOES fail the boot is different in kind and is handled
// elsewhere: a keyring that cannot be built at all (malformed key, a current
// version that is not in the ring) is a configuration error, config.Validate
// refuses it, and the process never reaches this file. That distinction is the
// whole design — "the operator typed the configuration wrong" is fatal, "the
// data needs a key the configuration does not have" is visible and survivable.
//
// # Why it repeats
//
// The startup pass is what an operator sees in the boot log after changing the
// keyring. The ticker is what makes the gauge follow a rotation that is running
// right now, in another process, so `secrets rotate` progress is visible on a
// dashboard rather than only in the terminal it was typed into.

// keyCensusInterval is how often the gauge is refreshed.
//
// One GROUP BY over an int column on a table with one row per connection. Five
// minutes is frequent enough to watch a rotation finish and rare enough to cost
// nothing; the numbers only change when an operator does something.
const keyCensusInterval = 5 * time.Minute

// keyCensusTimeout bounds one census so a slow database cannot leave a
// connection from the request pool held indefinitely.
const keyCensusTimeout = 30 * time.Second

// keyCensusStartupDelay is zero on purpose, unlike the audit sweep's.
//
// The audit sweep waits because a DELETE competing with boot makes readiness
// slower for no reason. This is one aggregate read, and its whole value is
// being in the boot log next to the line that says which keyring was loaded —
// an operator who has just restarted after editing SECRETS_KEYRING should not
// have to wait to learn whether the rows agree with what they typed.
const keyCensusStartupDelay = 0

// StartSecretKeyCensus reports key-version coverage at startup and keeps the
// gauge fresh.
//
// A no-op when the rotator is nil, which is the deployment with no keyring
// configured: no keyring means no sealed credentials, so there is nothing to
// take a census of.
//
// Like StartAuditRetention, the goroutine is not stopped on shutdown. It holds
// no client connection and blocks nothing; a census caught by the drain gets a
// closed-pool error, logs it, and the process exits.
func StartSecretKeyCensus(rotator *connection.Rotator) {
	if rotator == nil {
		return
	}

	go func() {
		if keyCensusStartupDelay > 0 {
			time.Sleep(keyCensusStartupDelay)
		}
		runSecretKeyCensus(rotator)

		ticker := time.NewTicker(keyCensusInterval)
		defer ticker.Stop()
		for range ticker.C {
			runSecretKeyCensus(rotator)
		}
	}()
}

// runSecretKeyCensus performs one census, publishes the gauge, and logs
// anything an operator needs to act on.
func runSecretKeyCensus(rotator *connection.Rotator) {
	ctx, cancel := context.WithTimeout(context.Background(), keyCensusTimeout)
	defer cancel()

	census, err := rotator.Census(ctx)
	if err != nil {
		// Not fatal and not a readiness failure: the census is diagnostics. A
		// database that cannot answer this is already failing the readiness
		// probe's ping, which is the check that exists for it.
		log.Warn("secret key census failed: " + err.Error())
		return
	}

	counts := make(map[int]uint64, len(census.Rows))
	for version, n := range census.Rows {
		if n < 0 {
			continue
		}
		counts[version] = uint64(n)
	}
	metrics.Default.SetSecretKeyVersionRows(counts)

	ring := rotator.Keyring()
	missing := census.Unopenable(ring)
	if len(missing) == 0 {
		log.Info("secret keyring covers every persisted credential (" +
			describeCensus(census, ring) + ")")
		return
	}

	// The one condition worth shouting about. It names versions and counts, and
	// nothing else — no connection ids here, because this line is emitted every
	// five minutes and a per-row list would be a log flood. `secrets status`
	// and `secrets rotate` are where the per-connection detail lives.
	var affected int64
	for _, v := range missing {
		affected += census.Rows[v]
	}
	log.Error("SECRET KEY MISSING: " + strconv.FormatInt(affected, 10) +
		" connection credential(s) are sealed under key version(s) " + joinVersions(missing) +
		", which this process does not hold. Those workspaces will answer " +
		"credentials_unavailable until the key material is restored to SECRETS_KEYRING. " +
		"Every other workspace is unaffected. Run `secrets status` for the breakdown.")
}

// describeCensus renders the distribution for one log line.
func describeCensus(census connection.KeyVersionCensus, ring interface{ CurrentVersion() int }) string {
	if census.Total() == 0 {
		return "no credentials stored yet; current key v" + strconv.Itoa(ring.CurrentVersion())
	}

	parts := make([]string, 0, len(census.Rows))
	for _, v := range census.Versions() {
		label := "v" + strconv.Itoa(v) + "=" + strconv.FormatInt(census.Rows[v], 10)
		if v == ring.CurrentVersion() {
			label += " (current)"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

// joinVersions renders a version list for a message.
func joinVersions(versions []int) string {
	parts := make([]string, 0, len(versions))
	for _, v := range versions {
		parts = append(parts, "v"+strconv.Itoa(v))
	}
	return strings.Join(parts, ", ")
}
