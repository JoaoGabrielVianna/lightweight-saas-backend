package authz

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// The server half of the capability-completeness gate.
//
// The Go SDK (sdk/go) ships apicoverage.json: a decision, per Project-accessible
// route, about whether the SDK serves it or deliberately does not. The failure
// this gate exists to prevent is quiet and specific:
//
//	someone adds a scoped route in a future slice
//	  → the registry classifies it, the OpenAPI document describes it
//	  → the SDK never hears about it
//	  → nothing fails, and the capability is invisible to every Go consumer
//
// So the registry — which is already the runtime source of truth and is already
// validated at boot — is compared against the SDK's decisions here. Adding a
// scoped route now costs one line in a JSON file, and refusing to write that
// line is itself a decision the reviewer sees.
//
// This is NOT a requirement that every route get an SDK method. `unsupported`
// with a reason passes. What may not happen is silence.
//
// # Why a JSON file rather than an import
//
// The SDK is a separate Go module with no dependency on this one, on purpose:
// that is what proves it could be extracted and what stops server types leaking
// into a public client API. A test that imported it would undo that. Reading a
// data file costs the two sides agreeing on a small schema and keeps the module
// boundary intact.

const sdkCoveragePath = "../../sdk/go/apicoverage.json"

type sdkCoverageEntry struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
	SDK    string `json:"sdk"`
	Reason string `json:"reason"`
}

type sdkCoverageManifest struct {
	Routes []sdkCoverageEntry `json:"routes"`
}

func loadSDKCoverage(t *testing.T) map[string]sdkCoverageEntry {
	t.Helper()

	raw, err := os.ReadFile(sdkCoveragePath)
	if err != nil {
		t.Fatalf("read %s: %v\n\nThe Go SDK's coverage manifest is missing. If the SDK was removed, "+
			"remove this gate deliberately rather than letting it fail.", sdkCoveragePath, err)
	}
	var m sdkCoverageManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", sdkCoveragePath, err)
	}
	if len(m.Routes) == 0 {
		t.Fatalf("%s declares no routes; the comparison would pass vacuously", sdkCoveragePath)
	}

	out := make(map[string]sdkCoverageEntry, len(m.Routes))
	for _, e := range m.Routes {
		out[routeKey(strings.ToUpper(e.Method), e.Path)] = e
	}
	return out
}

// projectReachableRoutes returns every route a project credential may call.
func projectReachableRoutes() []string {
	var out []string
	for _, key := range RegisteredRoutes() {
		method, path, _ := strings.Cut(key, " ")
		if req, ok := RequirementFor(method, path); ok && !req.OperatorOnly {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// TestSDKCoverage_EveryProjectReachableRouteIsClassified is the drift gate.
func TestSDKCoverage_EveryProjectReachableRouteIsClassified(t *testing.T) {
	manifest := loadSDKCoverage(t)

	reachable := projectReachableRoutes()
	if len(reachable) == 0 {
		t.Fatal("the registry classifies no route as project-reachable; the comparison would pass vacuously")
	}

	for _, key := range reachable {
		entry, ok := manifest[key]
		if !ok {
			t.Errorf("%s is reachable by a project credential but %s has no entry for it.\n\n"+
				"  Add one. `\"status\": \"supported\"` with the SDK method that serves it, or\n"+
				"  `\"status\": \"unsupported\"` with a reason. An endpoint no Go consumer can\n"+
				"  discover is a capability the product does not have in practice.",
				key, sdkCoveragePath)
			continue
		}

		method, path, _ := strings.Cut(key, " ")
		req, _ := RequirementFor(method, path)
		if entry.Scope != string(req.Scope) {
			t.Errorf("%s: the manifest records scope %q, the registry enforces %q.\n"+
				"  The SDK would document the wrong scope, and a developer would ask their\n"+
				"  operator for a key that cannot make the call.",
				key, entry.Scope, req.Scope)
		}
	}
}

// TestSDKCoverage_NoEntryForARouteThatDoesNotExist is the reverse direction.
//
// A manifest entry for a removed or renamed route is a claim of coverage for
// something unreachable — and, worse, it keeps the count looking complete while
// the route it replaced goes unclassified.
func TestSDKCoverage_NoEntryForARouteThatDoesNotExist(t *testing.T) {
	manifest := loadSDKCoverage(t)

	for key, entry := range manifest {
		method, path, _ := strings.Cut(key, " ")
		req, ok := RequirementFor(method, path)
		if !ok {
			t.Errorf("%s appears in %s but is not a registered /v1 route.\n"+
				"  Either the route was removed and the entry is stale, or the path is a typo\n"+
				"  and some real route is going unclassified behind it.", key, sdkCoveragePath)
			continue
		}
		if req.OperatorOnly {
			t.Errorf("%s appears in %s but is OPERATOR ONLY.\n\n"+
				"  This SDK authenticates with a Project Credential, which can never call this\n"+
				"  route. Shipping a method for it would produce a 403 that no scope can fix.\n"+
				"  (manifest status: %q)", key, sdkCoveragePath, entry.Status)
		}
	}
}

// TestSDKCoverage_SupportedRoutesAreDocumentedInOpenAPI.
//
// The SDK must not target an undocumented public route. A method whose endpoint
// is absent from the spec is one a developer cannot verify with curl, cannot
// read the error contract for, and cannot tell from an internal endpoint that
// happens to answer.
//
// The existing gate already ties the registry to the OpenAPI document; this ties
// the SDK's own list to it directly, so the SDK cannot drift even if a route's
// registry entry and its annotation drift together.
func TestSDKCoverage_SupportedRoutesAreDocumentedInOpenAPI(t *testing.T) {
	manifest := loadSDKCoverage(t)
	ops := swaggerV1Operations(t)

	for key, entry := range manifest {
		if entry.Status != "supported" {
			continue
		}
		op, ok := ops[key]
		if !ok {
			t.Errorf("%s is served by the SDK (%s) but is absent from the OpenAPI document.\n"+
				"  Run `make swagger`.", key, entry.SDK)
			continue
		}
		if !op.projectKey {
			t.Errorf("%s is served by the SDK (%s) but the OpenAPI document does not offer\n"+
				"  ProjectKeyAuth on it. One of the two is wrong, and a developer reading the\n"+
				"  spec would conclude the SDK method cannot work.", key, entry.SDK)
		}
	}
}

// TestSDKCoverage_DocumentedMatrixMatchesTheManifest.
//
// docs/SDK_GO.md publishes the matrix as a table, because a developer deciding
// whether to adopt the SDK reads documentation, not JSON. A hand-maintained
// table is exactly the kind of thing that goes stale the first time a method is
// renamed — and it goes stale INVISIBLY, since nothing else reads it.
//
// So the table is parsed and compared against the manifest. The manifest stays
// authoritative; this only stops the prose disagreeing with it.
func TestSDKCoverage_DocumentedMatrixMatchesTheManifest(t *testing.T) {
	const docPath = "../../docs/SDK_GO.md"

	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	manifest := loadSDKCoverage(t)

	// Rows look like: | `Users.List` | `GET` | `/v1/…` | `users:read` | supported |
	documented := map[string]string{} // routeKey -> sdk method
	for _, line := range strings.Split(string(raw), "\n") {
		cells := markdownRow(line)
		if len(cells) != 5 || !strings.HasPrefix(cells[2], "/v1/") {
			continue
		}
		// The document spells parameters {like_this}; the registry spells them
		// :like_this. One translation, here, rather than two sources of truth.
		path := strings.NewReplacer("{", ":", "}", "").Replace(cells[2])
		documented[routeKey(cells[1], path)] = cells[0]
	}

	if len(documented) == 0 {
		t.Fatalf("no matrix rows found in %s; the comparison would pass vacuously", docPath)
	}

	for key, entry := range manifest {
		method, ok := documented[key]
		if !ok {
			t.Errorf("%s is in the manifest but missing from the matrix in %s", key, docPath)
			continue
		}
		// The document abbreviates UsersService.List to Users.List, which reads
		// better and matches how the method is actually called.
		want := strings.Replace(entry.SDK, "Service.", ".", 1)
		if entry.Status == "supported" && method != want {
			t.Errorf("%s: the matrix says %q, the manifest says %q", key, method, want)
		}
	}
	for key := range documented {
		if _, ok := manifest[key]; !ok {
			t.Errorf("%s appears in the matrix in %s but not in the manifest", key, docPath)
		}
	}
}

// markdownRow splits a table row into trimmed cells with backticks stripped,
// returning nil for anything that is not a data row.
func markdownRow(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.Trim(strings.TrimSpace(p), "`"))
	}
	return out
}

// TestSDKCoverage_MatrixIsCompleteAndCounted guards against the whole manifest
// quietly shrinking.
//
// The checks above are per-route, so a manifest emptied of everything except one
// entry would pass each of them for the entry that remained and fail loudly for
// the rest — which is fine. What this adds is a single readable assertion of the
// totals, printed on failure, so the diff between "the SDK covers the surface"
// and "the SDK covers a corner of it" is one line rather than a scroll.
func TestSDKCoverage_MatrixIsCompleteAndCounted(t *testing.T) {
	manifest := loadSDKCoverage(t)
	reachable := projectReachableRoutes()

	var supported, unsupported int
	for _, entry := range manifest {
		switch entry.Status {
		case "supported":
			supported++
		case "unsupported":
			unsupported++
		}
	}

	if got := supported + unsupported; got != len(reachable) {
		t.Errorf("%d project-reachable routes, %d classified (%d supported, %d unsupported)",
			len(reachable), got, supported, unsupported)
		for _, key := range reachable {
			if _, ok := manifest[key]; !ok {
				t.Logf("  unclassified: %s", key)
			}
		}
	}
}
