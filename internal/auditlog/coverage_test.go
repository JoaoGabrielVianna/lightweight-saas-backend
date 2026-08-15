package auditlog

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
)

// The audit completeness gate.
//
// Slice 10's Phase 0 found eleven control-plane mutations emitting no audit
// event at all, including `connection.activated` — which redirects an entire
// workspace to a different Keycloak realm. Nobody decided that. The routes were
// added, the audit call was not, and no mechanism noticed for three slices.
//
// These tests are that mechanism. They mirror the authorization registry's
// completeness check, and for the same reason: a property that must hold for
// every route, verified by reading diffs, holds until the week someone is busy.

// TestCoverage_EveryMutatingRouteIsClassified is the gate.
//
// It walks the AUTHORIZATION registry rather than a list of its own. That
// registry is already proven complete against the routes gin actually mounted —
// `assertEveryV1RouteIsClassified` panics the boot otherwise — so using it as
// the source means this test inherits that completeness instead of maintaining
// a second copy that could fall behind.
func TestCoverage_EveryMutatingRouteIsClassified(t *testing.T) {
	mounted := authz.RegisteredRoutes()
	if len(mounted) == 0 {
		t.Fatal("the authorization registry is empty; this gate would pass vacuously")
	}

	mutating := 0
	for _, route := range mounted {
		method, _, _ := strings.Cut(route, " ")
		if IsMutating(method) {
			mutating++
		}
	}
	if mutating == 0 {
		t.Fatal("found no mutating routes; the method filter is wrong and this gate is asleep")
	}

	for _, route := range ValidateCoverage(mounted) {
		t.Errorf("%s changes state and has no audit classification.\n"+
			"  Add an entry to the registry in coverage.go: either audited(<event>), or\n"+
			"  notAudited(\"<why>\") if this mutation genuinely should record nothing.\n"+
			"  Preference is strongly toward auditing — an IAM mutation nobody can\n"+
			"  reconstruct afterwards is the gap this slice exists to close.", route)
	}
}

// TestCoverage_HasNoStaleEntries — the reverse direction.
//
// An entry for a route that no longer exists is worse than none: it makes the
// registry look complete while describing a surface that is gone, and the next
// person reads it as the specification it claims to be.
func TestCoverage_HasNoStaleEntries(t *testing.T) {
	mounted := map[string]bool{}
	for _, route := range authz.RegisteredRoutes() {
		mounted[route] = true
	}

	for _, route := range ClassifiedRoutes() {
		if !mounted[route] {
			t.Errorf("the audit registry classifies %s, which is not a mounted /v1 route.\n"+
				"  Either the route was removed and this entry is stale, or it is misspelled\n"+
				"  and the real route is silently unclassified.", route)
		}
	}
}

// TestCoverage_NoReadRouteIsClassified.
//
// Audit is not a request log. A GET in this registry would mean somebody
// started recording reads, and reads are what would drown the mutations the
// trail exists for — a console polling the audit page would generate more
// history than the operations it is displaying.
func TestCoverage_NoReadRouteIsClassified(t *testing.T) {
	for _, route := range ClassifiedRoutes() {
		method, path, _ := strings.Cut(route, " ")
		if !IsMutating(method) {
			t.Errorf("%s is a read and must not be audited. Reading history must not "+
				"create history; access is already in the request log.", path)
		}
	}
}

// TestCoverage_EveryEntryIsOneThingOrTheOther — a classification that both
// names an event and gives a reason for recording nothing is not a decision.
func TestCoverage_EveryEntryIsOneThingOrTheOther(t *testing.T) {
	for _, route := range ClassifiedRoutes() {
		method, path, _ := strings.Cut(route, " ")
		c, _ := CoverageFor(method, path)

		switch {
		case c.Event != "" && c.NotAuditedBecause != "":
			t.Errorf("%s both declares event %q and a reason for not auditing", route, c.Event)
		case c.Event == "" && c.NotAuditedBecause == "":
			t.Errorf("%s has an empty classification", route)
		}
	}
}

// TestCoverage_DeclaredEventsExistInTheVocabulary.
//
// A typo'd event name would classify the route, satisfy the gate, and record an
// event no filter matches. Comparing against the constants means a declaration
// can only name an action the audit package actually defines.
func TestCoverage_DeclaredEventsExistInTheVocabulary(t *testing.T) {
	known := knownAuditActions(t)

	for _, route := range ClassifiedRoutes() {
		method, path, _ := strings.Cut(route, " ")
		c, _ := CoverageFor(method, path)
		if c.Event == "" {
			continue
		}
		if !known[string(c.Event)] {
			t.Errorf("%s declares event %q, which is not an audit.Action constant.\n"+
				"  Known: %v", route, c.Event, sortedKeys(known))
		}
	}
}

// TestCoverage_TheControlPlaneIsFullyAudited.
//
// The specific regression Slice 10 found, pinned by name. The generic gate
// above would catch it too — but only as "some route is unclassified", and this
// says which class of route and why it matters, so a future reader of a failure
// knows what they broke.
func TestCoverage_TheControlPlaneIsFullyAudited(t *testing.T) {
	// Every route that creates, changes or destroys the objects governing WHO
	// can reach this API and WHICH realm a workspace routes through.
	controlPlane := []string{
		"POST /v1/workspaces",
		"PATCH /v1/workspaces/:workspace_id",
		"POST /v1/workspaces/:workspace_id/archive",

		"POST /v1/workspaces/:workspace_id/connections",
		"PATCH /v1/workspaces/:workspace_id/connections/:connection_id",
		"DELETE /v1/workspaces/:workspace_id/connections/:connection_id",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/verify",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/activate",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/retire",

		"POST /v1/workspaces/:workspace_id/projects",
		"PATCH /v1/workspaces/:workspace_id/projects/:project_id",
		"POST /v1/workspaces/:workspace_id/projects/:project_id/archive",
		"POST /v1/workspaces/:workspace_id/projects/:project_id/credentials",
		"POST /v1/workspaces/:workspace_id/projects/:project_id/credentials/:credential_id/revoke",
	}

	for _, route := range controlPlane {
		method, path, _ := strings.Cut(route, " ")
		c, ok := CoverageFor(method, path)
		if !ok {
			t.Errorf("control-plane route %s has no audit classification", route)
			continue
		}
		if c.Event == "" {
			t.Errorf("control-plane route %s records nothing (%q).\n"+
				"  These decide who can reach this API and which realm a workspace uses.\n"+
				"  An unrecorded change to one cannot be reconstructed from anything else.",
				route, c.NotAuditedBecause)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// knownAuditActions reads the Action constants from the audit package's source.
//
// Parsed rather than listed. A maintained list here would be a third copy of
// the vocabulary — after the constants and the coverage registry — and the
// whole point of this file is that copies drift.
func knownAuditActions(t *testing.T) map[string]bool {
	t.Helper()

	src, err := os.ReadFile("../audit/event.go")
	if err != nil {
		t.Fatalf("read the audit event catalogue: %v", err)
	}

	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`Action\s*=\s*"([a-z_]+\.[a-z_]+)"`).
		FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("found no Action constants; the pattern is wrong and this gate is asleep")
	}
	return out
}

// ─── Durability (Slice 15 / TD-033) ─────────────────────────────────────────

// TestCoverage_DurabilityMatchesWhereTheStateLives.
//
// The implication runs both ways, and both directions are a mistake someone
// could plausibly make:
//
//	control-plane marked best-effort  gives up a guarantee it can actually keep
//	provider marked transactional     CLAIMS a guarantee no PostgreSQL
//	                                  transaction can deliver — which is worse,
//	                                  because it would be believed
//
// The classification is derived here from the route's own domain rather than
// from the flag, so a row that sets ControlPlane on a Keycloak mutation fails
// even though nothing else in the system would notice.
func TestCoverage_DurabilityMatchesWhereTheStateLives(t *testing.T) {
	// The control plane is exactly the four domains whose rows are in this
	// database. Everything else under a workspace is provider state.
	postgresOwned := func(path string) bool {
		switch {
		case path == v1Workspaces, path == v1Workspace, path == v1Workspace+"/archive":
			return true
		case strings.Contains(path, "/connections"):
			return true
		case strings.Contains(path, "/projects"):
			return true
		}
		return false
	}

	for _, key := range ClassifiedRoutes() {
		method, path, _ := strings.Cut(key, " ")
		c, _ := CoverageFor(method, path)

		want := postgresOwned(path)
		switch {
		case want && !c.ControlPlane:
			t.Errorf("%s mutates state in THIS database and is not marked ControlPlane.\n"+
				"  It can be transactional with its audit row; declaring it best-effort gives up\n"+
				"  a guarantee for nothing. Use atomic(...) and add its acceptance case to\n"+
				"  atomicity_integration_test.go.", key)
		case !want && c.ControlPlane:
			t.Errorf("%s mutates a Keycloak realm and is marked ControlPlane.\n"+
				"  No PostgreSQL transaction can roll back a provider write, so this classification\n"+
				"  is a promise the system cannot keep. Use audited(...).", key)
		}
	}
}

// TestCoverage_ControlPlaneCountIsStable is the canary.
//
// The per-route checks are exhaustive, so a registry emptied to one entry fails
// loudly for the rest. What this adds is one readable number, so the difference
// between "the control plane is atomic" and "a corner of it is" is a single
// line rather than a scroll.
//
// The numbers may grow. Growing them means editing this test, which is exactly
// the moment to ask whether the new mutation got its acceptance case.
func TestCoverage_ControlPlaneCountIsStable(t *testing.T) {
	const (
		wantMutating     = 29
		wantControlPlane = 14
	)

	if got := len(ClassifiedRoutes()); got != wantMutating {
		t.Errorf("classified mutating routes = %d, want %d", got, wantMutating)
	}
	if got := len(ControlPlaneRoutes()); got != wantControlPlane {
		t.Errorf("control-plane mutations = %d, want %d.\n"+
			"  If one was added, give it a success case AND an audit-failure rollback case in\n"+
			"  atomicity_integration_test.go before updating this number.", got, wantControlPlane)
	}
}

// TestCoverage_EveryControlPlaneMutationHasAnAcceptanceCase.
//
// The atomicity suite is build-tagged, so a control-plane operation could be
// declared transactional and never actually exercised — the guarantee would be
// a comment. This ties the two together by event name.
//
// A string search over a source file is a blunt instrument and it is the right
// one here: the alternative is a registry of test names, which is a third list
// to keep in step with the first two. What it catches is the real failure — a
// new control-plane mutation whose acceptance case nobody wrote.
func TestCoverage_EveryControlPlaneMutationHasAnAcceptanceCase(t *testing.T) {
	const path = "atomicity_integration_test.go"

	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\n\nThe atomicity acceptance suite is missing. If it was removed, "+
			"remove this gate deliberately rather than letting it fail.", path, err)
	}
	text := string(source)

	// The acceptance suite names events by their CONSTANT, not their value, so
	// the search has to be for the identifier. The mapping is parsed from the
	// audit package rather than listed here, for the same reason
	// knownAuditActions parses it: a list would be a third copy of the
	// vocabulary, and copies drift.
	identifiers := auditActionIdentifiers(t)

	for _, key := range ControlPlaneRoutes() {
		method, routePath, _ := strings.Cut(key, " ")
		c, _ := CoverageFor(method, routePath)

		ident, ok := identifiers[string(c.Event)]
		if !ok {
			t.Errorf("%s declares event %q, which has no constant in the audit package", key, c.Event)
			continue
		}
		if !strings.Contains(text, ident) {
			t.Errorf("%s is declared transactional and audit.%s appears nowhere in %s.\n"+
				"  The guarantee is only real where it is exercised: add the success case and the\n"+
				"  audit-failure rollback case for this operation.", key, ident, path)
		}
	}
}

// auditActionIdentifiers maps an action's VALUE to its Go constant name.
func auditActionIdentifiers(t *testing.T) map[string]string {
	t.Helper()

	src, err := os.ReadFile("../audit/event.go")
	if err != nil {
		t.Fatalf("read the audit event catalogue: %v", err)
	}

	out := map[string]string{}
	for _, m := range regexp.MustCompile(`(Action[A-Za-z]+)\s+Action\s*=\s*"([a-z_]+\.[a-z_]+)"`).
		FindAllStringSubmatch(string(src), -1) {
		out[m[2]] = m[1]
	}
	if len(out) == 0 {
		t.Fatal("parsed no action constants; the search below would pass vacuously")
	}
	return out
}
