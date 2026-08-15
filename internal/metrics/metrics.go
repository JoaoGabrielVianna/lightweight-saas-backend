// Package metrics is the minimum an operator needs to answer "is it working,
// and if not, where".
//
// # Scope, and what it is not
//
// It is not observability. There is no tracing, no exemplars, no push gateway,
// no OpenTelemetry pipeline — all explicitly out of scope, and all things a
// self-hosted single-binary product should not require before it can be run.
// This closes the useful half of [TD-009]: enough numbers to see traffic,
// errors and refusals, exposed in a format something can scrape.
//
// # Why no Prometheus client library
//
// A dependency was considered and rejected, and the reasoning is the trade
// rather than a preference:
//
//	what it would give   histograms with correct exposition, a registry, a
//	                     handler, and no chance of getting the text format wrong
//	what it would cost   prometheus/client_golang pulls client_model, common,
//	                     procfs and protobuf into a module whose entire current
//	                     dependency set is fifteen direct entries, for a product
//	                     that ships as one binary to a VPS
//
// The deciding factor is that what is exposed here is SMALL and CLOSED: three
// metric families, label values drawn from the route table rather than from
// input, and no summaries or quantiles. The text format for counters and one
// histogram is about sixty lines and is exercised by a test that parses what it
// produces. Had there been a need for exemplars, native histograms or a
// registry shared with third-party collectors, the library would be the right
// answer — and it still is, the day that need arrives, because nothing here
// leaks into its callers beyond three function calls.
//
// # Cardinality
//
// Every label value is bounded by something the SERVER controls:
//
//	method  a fixed set of HTTP verbs
//	route   gin's registered pattern, e.g. /v1/workspaces/:workspace_id/users
//	status  the response code
//
// The route is the PATTERN and never the concrete path, which is the difference
// between fifty series and one per workspace id ever seen. Nothing derived from
// a caller — no user id, request id, credential id, project id or workspace id —
// is a label anywhere in this package, and TestMetrics_NoIdentifiersInLabels
// checks the rendered output for exactly those.
//
// [TD-009]: docs/TECH_DEBT.md#td-009
package metrics

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// durationBuckets are the histogram's upper bounds, in seconds.
//
// Chosen against the measured behaviour of this API rather than a default
// ladder: Slice 8 measured a project read at ~12ms p50 / ~116ms p99 and a write
// at ~25ms / ~292ms, with refusals under 2ms. The buckets are dense where those
// live, so the difference between "normal" and "the provider got slow" is
// visible, and sparse above one second, where the only question is how bad.
var durationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// Registry holds the counters. One per process; the package-level Default is
// what the middleware uses.
//
// A plain mutex over maps rather than sharded atomics: the write is a map
// lookup and an increment, the read happens once per scrape, and a lock-free
// design here would be optimising the wrong thing while making the
// exposition — which must see a consistent snapshot — harder to get right.
type Registry struct {
	mu sync.Mutex

	requests   map[requestKey]uint64
	durations  map[routeKey]*histogram
	authFail   map[string]uint64
	denials    map[string]uint64
	auditFail  map[string]uint64
	secretFail map[string]uint64
	keyRows    map[int]uint64
}

type requestKey struct {
	method string
	route  string
	status int
}

type routeKey struct {
	method string
	route  string
}

type histogram struct {
	counts []uint64 // one per bucket, cumulative computed at render time
	sum    float64
	total  uint64
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		requests:   map[requestKey]uint64{},
		durations:  map[routeKey]*histogram{},
		authFail:   map[string]uint64{},
		denials:    map[string]uint64{},
		auditFail:  map[string]uint64{},
		secretFail: map[string]uint64{},
		keyRows:    map[int]uint64{},
	}
}

// Default is the process registry.
var Default = NewRegistry()

// maxRouteLabels bounds how many distinct routes the registry will track.
//
// The route label comes from gin's route table, so it is already bounded — but
// "already bounded" is an assumption about a caller, and an unbounded map
// behind a public function is how a metrics endpoint becomes a memory leak. If
// the bound is ever hit, further routes collapse into "other" rather than
// growing the map: losing resolution beats losing the process.
const maxRouteLabels = 500

// otherRoute is where an unmatched or over-budget route is counted. An
// unmatched request has no registered pattern, and using its raw path would
// make every 404 from a scanner a new time series.
const otherRoute = "other"

// ObserveRequest records one completed HTTP request.
//
// route must be the registered PATTERN. Passing a concrete path would be the
// cardinality bug this package exists to avoid, so the middleware is the only
// caller and it reads gin's FullPath.
func (r *Registry) ObserveRequest(method, route string, status int, seconds float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	route = r.boundRoute(route)

	r.requests[requestKey{method: method, route: route, status: status}]++

	k := routeKey{method: method, route: route}
	h := r.durations[k]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(durationBuckets)+1)}
		r.durations[k] = h
	}
	h.total++
	h.sum += seconds
	h.counts[bucketIndex(seconds)]++
}

// boundRoute enforces the label budget. Caller holds the lock.
func (r *Registry) boundRoute(route string) string {
	if route == "" {
		return otherRoute
	}
	if len(r.durations) < maxRouteLabels {
		return route
	}
	if _, known := r.durations[routeKey{method: "GET", route: route}]; known {
		return route
	}
	return otherRoute
}

// bucketIndex returns the bucket a duration falls in; the last index is +Inf.
func bucketIndex(seconds float64) int {
	for i, upper := range durationBuckets {
		if seconds <= upper {
			return i
		}
	}
	return len(durationBuckets)
}

// ObserveAuthFailure records a rejected authentication attempt.
//
// kind is the auth event kind — a fixed vocabulary from internal/auth, never a
// reason string, which would carry parser output and be unbounded.
func (r *Registry) ObserveAuthFailure(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.authFail) < maxRouteLabels {
		r.authFail[sanitizeLabel(kind)]++
	}
}

// ObserveAuthorizationDenial records a request refused after the caller was
// known. principal is "operator" or "project" — which of the two is failing is
// the first thing an operator wants to know, and it is a two-value set.
func (r *Registry) ObserveAuthorizationDenial(principal string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.denials) < maxRouteLabels {
		r.denials[sanitizeLabel(principal)]++
	}
}

// ObserveAuditPersistFailure records a durable audit write that failed.
//
// This is the metric the audit failure policy depends on. The policy is to
// succeed the response when the business mutation worked and only the audit
// write did not, because failing it would invite a retry of a mutation that
// already happened. That trade is only acceptable if the condition is
// ALERTABLE — a silently incomplete trail is worse than a loudly incomplete
// one, because it invites trust it has not earned.
//
// Labelled by event type, which is a closed vocabulary owned by internal/audit.
// Never by workspace, project, credential or event id.
func (r *Registry) ObserveAuditPersistFailure(eventType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.auditFail) < maxRouteLabels {
		r.auditFail[sanitizeLabel(eventType)]++
	}
}

// maxKeyVersionLabels bounds the key-version gauge.
//
// A keyring has a handful of versions and the number only grows when an
// operator rotates, so this will never be approached in practice. It is here
// because the values come from a database column, and a column is input: a row
// hand-edited to version 900000 must cost one dropped series, not a series per
// distinct integer someone can write.
const maxKeyVersionLabels = 64

// secretOpenReasons is the complete set of values the reason label may take.
//
// An ALLOWLIST rather than sanitisation, and this is the one metric where that
// distinction is load-bearing. sanitizeLabel bounds a value's shape: it strips
// characters that would break the exposition format and truncates to forty
// characters. That is enough for a route or an event type, and it is NOT enough
// here — a base64 master key is forty-four characters of exactly the alphabet
// sanitizeLabel preserves, so a mistaken caller would publish a decodable
// prefix of a key into a scrape that gets stored, indexed and retained.
//
// So this label cannot be sanitised into safety; it can only be constrained.
// Anything not on this list is counted as "other" and discarded, which turns
// the worst case from "a key is in the metrics" into "a series says other".
//
// Mirrors secrets.OpenFailureReason. The two are pinned separately —
// TestOpenFailureReason_VocabularyIsClosed in that package — rather than by an
// import, so this package keeps its independence from the domain and a drift
// surfaces as an "other" count instead of a leak.
var secretOpenReasons = map[string]bool{
	"unknown_key_version":   true,
	"authentication_failed": true,
	"unsupported_algorithm": true,
	"other":                 true,
}

// ObserveSecretOpenFailure records a sealed provider credential that could not
// be opened at request time.
//
// This is the metric that says a master key is missing from THIS process, which
// is a different fact from the rotation report — the rotation runs in a
// separate command, whose output nobody scrapes, and an operator who removed a
// key too early finds out here.
//
// reason must come from secrets.OpenFailureReason. Never the connection, the
// workspace or the version: the first two are unbounded, and the third is
// already carried by the key-version gauge.
func (r *Registry) ObserveSecretOpenFailure(reason string) {
	if !secretOpenReasons[reason] {
		reason = "other"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secretFail[reason]++
}

// SetSecretKeyVersionRows publishes how many persisted connection secrets are
// sealed under each key version.
//
// A gauge and a full replacement rather than a counter, because the question it
// answers is a level, not a rate: "how many rows still need the old key" is how
// an operator watches a rotation finish and decides the old key can be
// destroyed. Rows move between versions and the total can fall, so incrementing
// would be wrong.
//
// Fed by a periodic census (server.StartSecretKeyCensus), not by the request
// path — nothing here counts a row when it is read.
func (r *Registry) SetSecretKeyVersionRows(counts map[int]uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Replaced wholesale so a version that drops to zero rows disappears from
	// the exposition instead of being frozen at its last value. A stale series
	// reading "v1: 3" forever is precisely the reading that would stop an
	// operator from ever retiring v1.
	fresh := make(map[int]uint64, len(counts))
	versions := make([]int, 0, len(counts))
	for v := range counts {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	for _, v := range versions {
		if len(fresh) >= maxKeyVersionLabels {
			break
		}
		fresh[v] = counts[v]
	}
	r.keyRows = fresh
}

// sanitizeLabel keeps a label value to a safe, bounded shape.
//
// Defensive: every value passed today is a constant. It exists so that a future
// caller passing something dynamic degrades to a truncated, escaped string
// rather than breaking the exposition format or leaking a payload into a
// scrape.
func sanitizeLabel(v string) string {
	if v == "" {
		return "unknown"
	}
	if len(v) > 40 {
		v = v[:40]
	}
	var b strings.Builder
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Render writes the Prometheus text exposition format.
//
// Deterministic ordering, which matters for two reasons: a diffable output is
// testable, and Prometheus requires all series of one metric family to be
// contiguous.
func (r *Registry) Render() string {
	r.mu.Lock()
	snapshot := r.snapshot()
	r.mu.Unlock()

	var b strings.Builder
	snapshot.renderRequests(&b)
	snapshot.renderDurations(&b)
	snapshot.renderCounter(&b, "lightweight_auth_failures_total",
		"Authentication attempts rejected, by event kind.", "kind", snapshot.authFail)
	snapshot.renderCounter(&b, "lightweight_authorization_denials_total",
		"Requests refused after the caller was identified, by principal type.",
		"principal", snapshot.denials)
	snapshot.renderCounter(&b, "lightweight_audit_persist_failures_total",
		"Durable audit writes that failed after the mutation succeeded, by event type.",
		"event", snapshot.auditFail)
	snapshot.renderCounter(&b, "lightweight_secret_open_failures_total",
		"Sealed provider credentials this process could not open, by reason.",
		"reason", snapshot.secretFail)
	snapshot.renderKeyVersionRows(&b)
	return b.String()
}

// snap is a consistent copy taken under the lock, so rendering — which is the
// slow part — does not hold it.
type snap struct {
	requests   map[requestKey]uint64
	durations  map[routeKey]histogram
	authFail   map[string]uint64
	denials    map[string]uint64
	auditFail  map[string]uint64
	secretFail map[string]uint64
	keyRows    map[int]uint64
}

func (r *Registry) snapshot() snap {
	s := snap{
		requests:   make(map[requestKey]uint64, len(r.requests)),
		durations:  make(map[routeKey]histogram, len(r.durations)),
		authFail:   make(map[string]uint64, len(r.authFail)),
		denials:    make(map[string]uint64, len(r.denials)),
		auditFail:  make(map[string]uint64, len(r.auditFail)),
		secretFail: make(map[string]uint64, len(r.secretFail)),
		keyRows:    make(map[int]uint64, len(r.keyRows)),
	}
	for k, v := range r.secretFail {
		s.secretFail[k] = v
	}
	for k, v := range r.keyRows {
		s.keyRows[k] = v
	}
	for k, v := range r.requests {
		s.requests[k] = v
	}
	for k, v := range r.durations {
		c := make([]uint64, len(v.counts))
		copy(c, v.counts)
		s.durations[k] = histogram{counts: c, sum: v.sum, total: v.total}
	}
	for k, v := range r.authFail {
		s.authFail[k] = v
	}
	for k, v := range r.denials {
		s.denials[k] = v
	}
	for k, v := range r.auditFail {
		s.auditFail[k] = v
	}
	return s
}

func (s snap) renderRequests(b *strings.Builder) {
	b.WriteString("# HELP lightweight_http_requests_total HTTP requests completed, by method, route pattern and status.\n")
	b.WriteString("# TYPE lightweight_http_requests_total counter\n")

	keys := make([]requestKey, 0, len(s.requests))
	for k := range s.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].status < keys[j].status
	})

	for _, k := range keys {
		b.WriteString("lightweight_http_requests_total{method=\"" + escape(k.method) +
			"\",route=\"" + escape(k.route) +
			"\",status=\"" + strconv.Itoa(k.status) + "\"} " +
			strconv.FormatUint(s.requests[k], 10) + "\n")
	}
}

func (s snap) renderDurations(b *strings.Builder) {
	b.WriteString("# HELP lightweight_http_request_duration_seconds Request duration, by method and route pattern.\n")
	b.WriteString("# TYPE lightweight_http_request_duration_seconds histogram\n")

	keys := make([]routeKey, 0, len(s.durations))
	for k := range s.durations {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].method < keys[j].method
	})

	for _, k := range keys {
		h := s.durations[k]
		labels := "method=\"" + escape(k.method) + "\",route=\"" + escape(k.route) + "\""

		// Prometheus histogram buckets are CUMULATIVE: each le="x" reports
		// everything at or below x, not the count in that band.
		var cumulative uint64
		for i, upper := range durationBuckets {
			cumulative += h.counts[i]
			b.WriteString("lightweight_http_request_duration_seconds_bucket{" + labels +
				",le=\"" + strconv.FormatFloat(upper, 'g', -1, 64) + "\"} " +
				strconv.FormatUint(cumulative, 10) + "\n")
		}
		cumulative += h.counts[len(durationBuckets)]
		b.WriteString("lightweight_http_request_duration_seconds_bucket{" + labels +
			",le=\"+Inf\"} " + strconv.FormatUint(cumulative, 10) + "\n")
		b.WriteString("lightweight_http_request_duration_seconds_sum{" + labels + "} " +
			strconv.FormatFloat(h.sum, 'g', -1, 64) + "\n")
		b.WriteString("lightweight_http_request_duration_seconds_count{" + labels + "} " +
			strconv.FormatUint(h.total, 10) + "\n")
	}
}

// renderKeyVersionRows exposes the master-key rotation progress gauge.
//
// The version is an integer rendered as a label value, which is the one place
// in this package where a label comes from the database. It is bounded twice:
// by maxKeyVersionLabels when the gauge is set, and by the fact that a version
// is written by this process and CHECKed `>= 1` by the schema.
func (s snap) renderKeyVersionRows(b *strings.Builder) {
	b.WriteString("# HELP lightweight_secret_key_version_rows Persisted provider credentials, by the master-key version needed to open them.\n")
	b.WriteString("# TYPE lightweight_secret_key_version_rows gauge\n")

	versions := make([]int, 0, len(s.keyRows))
	for v := range s.keyRows {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	for _, v := range versions {
		b.WriteString("lightweight_secret_key_version_rows{version=\"" + strconv.Itoa(v) + "\"} " +
			strconv.FormatUint(s.keyRows[v], 10) + "\n")
	}
}

func (s snap) renderCounter(b *strings.Builder, name, help, label string, values map[string]uint64) {
	b.WriteString("# HELP " + name + " " + help + "\n")
	b.WriteString("# TYPE " + name + " counter\n")

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		b.WriteString(name + "{" + label + "=\"" + escape(k) + "\"} " +
			strconv.FormatUint(values[k], 10) + "\n")
	}
}

// escape applies the exposition format's label-value escaping. Backslash, quote
// and newline are the three characters that can break a scrape.
func escape(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}
