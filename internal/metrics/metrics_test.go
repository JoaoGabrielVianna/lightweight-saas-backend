package metrics

import (
	"strconv"
	"strings"
	"testing"
)

// The exposition format is hand-written, so it is parsed back and checked
// rather than eyeballed. A malformed line does not fail loudly at runtime — the
// scraper silently drops the series, and the dashboard is empty for a reason
// nobody can see.

// parseSamples reads the rendered output into name{labels} → value, and fails
// on any line that is not a comment or a well-formed sample.
func parseSamples(t *testing.T, rendered string) map[string]float64 {
	t.Helper()

	out := map[string]float64{}
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, value, found := strings.Cut(line, " ")
		if !found {
			t.Fatalf("line has no value separator: %q", line)
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("line %q has an unparseable value: %v", line, err)
		}
		if _, dup := out[series]; dup {
			t.Errorf("duplicate series %q — a scraper rejects the whole scrape", series)
		}
		out[series] = v
	}
	return out
}

func TestMetrics_CountsRequestsByMethodRouteAndStatus(t *testing.T) {
	r := NewRegistry()
	r.ObserveRequest("GET", "/v1/workspaces/:workspace_id/users", 200, 0.02)
	r.ObserveRequest("GET", "/v1/workspaces/:workspace_id/users", 200, 0.03)
	r.ObserveRequest("GET", "/v1/workspaces/:workspace_id/users", 429, 0.001)
	r.ObserveRequest("POST", "/v1/workspaces/:workspace_id/users", 201, 0.2)

	samples := parseSamples(t, r.Render())

	cases := map[string]float64{
		`lightweight_http_requests_total{method="GET",route="/v1/workspaces/:workspace_id/users",status="200"}`:  2,
		`lightweight_http_requests_total{method="GET",route="/v1/workspaces/:workspace_id/users",status="429"}`:  1,
		`lightweight_http_requests_total{method="POST",route="/v1/workspaces/:workspace_id/users",status="201"}`: 1,
	}
	for series, want := range cases {
		if got := samples[series]; got != want {
			t.Errorf("%s = %v, want %v", series, got, want)
		}
	}
}

// TestMetrics_HistogramBucketsAreCumulative — the single most likely mistake in
// a hand-written histogram. Prometheus defines le="x" as "everything at or
// below x"; per-band counts render without error and make every quantile wrong.
func TestMetrics_HistogramBucketsAreCumulative(t *testing.T) {
	r := NewRegistry()
	// One in the 0.005 bucket, one in 0.05, one above every bound.
	r.ObserveRequest("GET", "/x", 200, 0.004)
	r.ObserveRequest("GET", "/x", 200, 0.03)
	r.ObserveRequest("GET", "/x", 200, 30)

	samples := parseSamples(t, r.Render())
	const prefix = `lightweight_http_request_duration_seconds_bucket{method="GET",route="/x",le="`

	if got := samples[prefix+`0.005"}`]; got != 1 {
		t.Errorf("le=0.005 = %v, want 1", got)
	}
	if got := samples[prefix+`0.05"}`]; got != 2 {
		t.Errorf("le=0.05 = %v, want 2 (cumulative: it must include the 0.004 sample)", got)
	}
	if got := samples[prefix+`+Inf"}`]; got != 3 {
		t.Errorf("le=+Inf = %v, want 3", got)
	}

	// Cumulative counts must never decrease as the bound grows.
	var previous float64
	for _, upper := range durationBuckets {
		series := prefix + strconv.FormatFloat(upper, 'g', -1, 64) + `"}`
		got := samples[series]
		if got < previous {
			t.Errorf("bucket %v = %v, below the previous bucket's %v — not cumulative",
				upper, got, previous)
		}
		previous = got
	}

	if got := samples[`lightweight_http_request_duration_seconds_count{method="GET",route="/x"}`]; got != 3 {
		t.Errorf("_count = %v, want 3", got)
	}
	if got := samples[`lightweight_http_request_duration_seconds_sum{method="GET",route="/x"}`]; got < 30 {
		t.Errorf("_sum = %v, want at least 30", got)
	}
}

// TestMetrics_UnmatchedRouteCollapses — a request that matched no route has no
// pattern. Using its raw path would make every 404 from a scanner a new series,
// which is how a metrics endpoint becomes the outage.
func TestMetrics_UnmatchedRouteCollapses(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 50; i++ {
		r.ObserveRequest("GET", "", 404, 0.001)
	}

	samples := parseSamples(t, r.Render())
	if got := samples[`lightweight_http_requests_total{method="GET",route="other",status="404"}`]; got != 50 {
		t.Errorf("unmatched requests = %v, want all 50 under route=\"other\"", got)
	}
	if len(samples) > 20 {
		t.Errorf("50 unmatched requests produced %d series", len(samples))
	}
}

// TestMetrics_RouteLabelBudgetIsEnforced — the map is bounded even if a caller
// ignores the "pattern only" contract.
func TestMetrics_RouteLabelBudgetIsEnforced(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < maxRouteLabels*3; i++ {
		r.ObserveRequest("GET", "/generated/"+strconv.Itoa(i), 200, 0.01)
	}

	r.mu.Lock()
	tracked := len(r.durations)
	r.mu.Unlock()

	if tracked > maxRouteLabels+1 { // +1 for the "other" bucket
		t.Errorf("registry tracks %d routes, above the budget of %d", tracked, maxRouteLabels)
	}
}

// TestMetrics_NoIdentifiersInLabels is the cardinality and privacy rule stated
// as an assertion.
//
// The forbidden values are fed IN, so this fails if any code path ever puts one
// in a label. A test that only checked a clean registry would pass forever.
func TestMetrics_NoIdentifiersInLabels(t *testing.T) {
	r := NewRegistry()

	// Concrete ids, as they would appear in a real URL.
	r.ObserveRequest("GET", "/v1/workspaces/:workspace_id/users/:user_id", 200, 0.01)
	r.ObserveAuthFailure("validation_failed")
	r.ObserveAuthorizationDenial("project")

	rendered := r.Render()
	for _, forbidden := range []string{
		"ws_3f2504e0", "prj_7c9e6679", "key_9b2f4c1a",
		"9c1e6679-7425-40de", "lw_sk_",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the rendered metrics contain %q", forbidden)
		}
	}

	// The route label must be the PATTERN, colons and all.
	if !strings.Contains(rendered, ":workspace_id") {
		t.Error("the route label is not the registered pattern")
	}
}

// TestMetrics_LabelValuesAreSanitised — defensive, because every caller passes
// a constant today. A quote or a newline in a label value breaks the whole
// scrape, not just that line.
func TestMetrics_LabelValuesAreSanitised(t *testing.T) {
	r := NewRegistry()
	r.ObserveAuthFailure("bad\"value\nwith breaks")
	r.ObserveAuthorizationDenial(strings.Repeat("x", 500))

	rendered := r.Render()
	parseSamples(t, rendered) // fails the test if any line is malformed

	if strings.Contains(rendered, "with breaks") {
		t.Error("an unsanitised label value survived")
	}
	for _, line := range strings.Split(rendered, "\n") {
		if len(line) > 300 {
			t.Errorf("a rendered line is %d characters; a label value was not bounded", len(line))
		}
	}
}

// TestMetrics_RouteEscapingCannotBreakTheFormat — a route pattern with a quote
// cannot exist in gin, but escaping is what stops that assumption from being
// load-bearing.
func TestMetrics_RouteEscapingCannotBreakTheFormat(t *testing.T) {
	r := NewRegistry()
	r.ObserveRequest("GET", `/weird"route`, 200, 0.01)

	parseSamples(t, r.Render())
	if !strings.Contains(r.Render(), `\"`) {
		t.Error("a quote in a label value was not escaped")
	}
}

// TestMetrics_EveryFamilyIsDeclared — a sample with no preceding # TYPE is
// accepted by Prometheus but loses its type, which silently breaks rate() and
// histogram_quantile().
func TestMetrics_EveryFamilyIsDeclared(t *testing.T) {
	r := NewRegistry()
	r.ObserveRequest("GET", "/x", 200, 0.01)
	r.ObserveAuthFailure("validation_failed")
	r.ObserveAuthorizationDenial("operator")

	rendered := r.Render()
	for _, family := range []string{
		"lightweight_http_requests_total",
		"lightweight_http_request_duration_seconds",
		"lightweight_auth_failures_total",
		"lightweight_authorization_denials_total",
	} {
		if !strings.Contains(rendered, "# HELP "+family+" ") {
			t.Errorf("%s has no HELP line", family)
		}
		if !strings.Contains(rendered, "# TYPE "+family+" ") {
			t.Errorf("%s has no TYPE line", family)
		}
	}
}

// TestMetrics_EmptyRegistryRendersValidOutput — a freshly started process is
// scraped before it has served anything.
func TestMetrics_EmptyRegistryRendersValidOutput(t *testing.T) {
	samples := parseSamples(t, NewRegistry().Render())
	if len(samples) != 0 {
		t.Errorf("an empty registry produced %d samples", len(samples))
	}
}

// TestMetrics_ConcurrentObservationsDoNotRace — the middleware runs on every
// request, so the registry is written from every handler goroutine at once.
// Meaningful under -race, which the gates run.
func TestMetrics_ConcurrentObservationsDoNotRace(t *testing.T) {
	r := NewRegistry()

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				r.ObserveRequest("GET", "/v1/workspaces/:workspace_id/users", 200, 0.01)
				r.ObserveAuthFailure("validation_failed")
				if j%10 == 0 {
					_ = r.Render()
				}
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}

	samples := parseSamples(t, r.Render())
	if got := samples[`lightweight_http_requests_total{method="GET",route="/v1/workspaces/:workspace_id/users",status="200"}`]; got != 1600 {
		t.Errorf("counter = %v, want 1600 — increments were lost", got)
	}
}

// ---------------------------------------------------------------------------
// Master-key rotation metrics
// ---------------------------------------------------------------------------

// TestMetrics_SecretOpenFailuresAreCountedByReason. The reason is what tells an
// operator whether restoring a key will fix the problem, so it has to survive
// into the exposition — and it has to stay a closed vocabulary.
func TestMetrics_SecretOpenFailuresAreCountedByReason(t *testing.T) {
	r := NewRegistry()
	r.ObserveSecretOpenFailure("unknown_key_version")
	r.ObserveSecretOpenFailure("unknown_key_version")
	r.ObserveSecretOpenFailure("authentication_failed")

	samples := parseSamples(t, r.Render())
	if got := samples[`lightweight_secret_open_failures_total{reason="unknown_key_version"}`]; got != 2 {
		t.Errorf("unknown_key_version = %v, want 2", got)
	}
	if got := samples[`lightweight_secret_open_failures_total{reason="authentication_failed"}`]; got != 1 {
		t.Errorf("authentication_failed = %v, want 1", got)
	}
}

// TestMetrics_KeyVersionGaugeReplacesRatherThanAccumulates.
//
// This is the whole reason it is a gauge. Rows MOVE between versions during a
// rotation and the count for the old one falls to zero; a series frozen at its
// last non-zero value would tell an operator that v1 is still needed forever,
// which is exactly the reading that stops a key ever being retired.
func TestMetrics_KeyVersionGaugeReplacesRatherThanAccumulates(t *testing.T) {
	r := NewRegistry()

	r.SetSecretKeyVersionRows(map[int]uint64{1: 14, 2: 0})
	samples := parseSamples(t, r.Render())
	if got := samples[`lightweight_secret_key_version_rows{version="1"}`]; got != 14 {
		t.Errorf("v1 = %v, want 14", got)
	}

	// Rotation finishes: everything is on v2 and v1 has no rows at all.
	r.SetSecretKeyVersionRows(map[int]uint64{2: 14})
	samples = parseSamples(t, r.Render())
	if _, present := samples[`lightweight_secret_key_version_rows{version="1"}`]; present {
		t.Error("v1 is still exposed after every row moved off it — a stale series that " +
			"would tell an operator the old key is still needed")
	}
	if got := samples[`lightweight_secret_key_version_rows{version="2"}`]; got != 14 {
		t.Errorf("v2 = %v, want 14", got)
	}
}

// TestMetrics_KeyVersionGaugeIsBounded. The version comes from a database
// column, and a column is input.
func TestMetrics_KeyVersionGaugeIsBounded(t *testing.T) {
	r := NewRegistry()

	counts := make(map[int]uint64, maxKeyVersionLabels*3)
	for v := 1; v <= maxKeyVersionLabels*3; v++ {
		counts[v] = 1
	}
	r.SetSecretKeyVersionRows(counts)

	rendered := r.Render()
	parseSamples(t, rendered)

	series := strings.Count(rendered, "lightweight_secret_key_version_rows{")
	if series > maxKeyVersionLabels {
		t.Errorf("%d key-version series exposed, want at most %d", series, maxKeyVersionLabels)
	}
}

// TestMetrics_RotationMetricsCarryNoIdentifiers.
//
// The gauge's label is a version — an integer — and the counter's is a reason
// from a closed vocabulary. Neither may carry a connection, a workspace or
// anything else unbounded, so the forbidden values are fed IN.
func TestMetrics_RotationMetricsCarryNoIdentifiers(t *testing.T) {
	r := NewRegistry()

	r.ObserveSecretOpenFailure("unknown_key_version")
	r.SetSecretKeyVersionRows(map[int]uint64{1: 3, 2: 11})

	rendered := r.Render()
	for _, forbidden := range []string{
		"conn_3f2504e0", "ws_7c9e6679", "prj_", "lw_sk_",
		"7425-40de", "saas-realm",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the rendered metrics contain %q", forbidden)
		}
	}
	if !strings.Contains(rendered, `lightweight_secret_key_version_rows{version="2"} 11`) {
		t.Errorf("the gauge is not exposed as expected:\n%s", rendered)
	}
}

// TestMetrics_SecretReasonLabelIsAnAllowlistNotASanitiser.
//
// The distinction this pins is the reason ObserveSecretOpenFailure does not use
// sanitizeLabel like its neighbours. A base64 master key is forty-four
// characters drawn almost entirely from the alphabet sanitizeLabel PRESERVES,
// so sanitising one leaves a forty-character decodable prefix — published into
// a scrape that is then stored, indexed and retained.
//
// An allowlist has no such failure mode. Anything unrecognised becomes "other".
func TestMetrics_SecretReasonLabelIsAnAllowlistNotASanitiser(t *testing.T) {
	r := NewRegistry()

	// A real 32-byte key's shape: 44 characters, base64 alphabet.
	const keyShaped = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	r.ObserveSecretOpenFailure(keyShaped)
	r.ObserveSecretOpenFailure("conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301")
	r.ObserveSecretOpenFailure("unknown_key_version")

	rendered := r.Render()
	parseSamples(t, rendered)

	if strings.Contains(rendered, keyShaped) {
		t.Error("a base64-shaped label was published verbatim")
	}
	// Sanitisation alone would have left this prefix; the allowlist does not.
	if strings.Contains(rendered, keyShaped[:40]) {
		t.Error("a 40-character prefix of the key survived — this label is being " +
			"sanitised rather than constrained, and a scrape now holds most of a master key")
	}
	if strings.Contains(rendered, "conn_3f2504e0") {
		t.Error("a connection id reached a label")
	}

	samples := parseSamples(t, rendered)
	if got := samples[`lightweight_secret_open_failures_total{reason="other"}`]; got != 2 {
		t.Errorf("other = %v, want 2 — both unrecognised values must collapse there", got)
	}
	if got := samples[`lightweight_secret_open_failures_total{reason="unknown_key_version"}`]; got != 1 {
		t.Errorf("a legitimate reason was not counted: %v", got)
	}
}
