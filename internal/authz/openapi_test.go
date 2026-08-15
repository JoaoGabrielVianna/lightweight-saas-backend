package authz

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// The OpenAPI document and the authorization registry must agree.
//
// They are two statements of the same fact — "may a project credential call
// this, and with which scope" — written in two places by two mechanisms. The
// registry is enforced at runtime and validated at boot. The Swagger document
// is generated from annotations a human writes above each handler, so it is the
// half that drifts, and it drifts SILENTLY: nothing fails, the API keeps
// behaving correctly, and only the documentation becomes wrong.
//
// A wrong document here is not cosmetic. It is the input to an SDK generator
// and the thing a developer reads before writing an integration. Two failure
// directions, both worth catching:
//
//	declared but refused  the document offers ProjectKeyAuth on a route the
//	                      registry marks operator-only. A developer writes the
//	                      integration, ships it, and gets 403 in production.
//	refused but usable    the document omits ProjectKeyAuth on a scoped route.
//	                      A capability the product has is invisible, and the
//	                      developer builds a worse workaround.
//
// Swagger 2.0 cannot express per-operation scopes for an apiKey scheme, so the
// scope is stated in prose. That makes it MORE likely to drift, not less, which
// is why the prose is checked here too rather than trusted.

const swaggerPath = "../../docs/swagger.json"

type swaggerSpec struct {
	Paths map[string]map[string]struct {
		Description string                `json:"description"`
		Security    []map[string][]string `json:"security"`
	} `json:"paths"`
	SecurityDefinitions map[string]struct {
		Description string `json:"description"`
	} `json:"securityDefinitions"`
}

// swaggerV1Operations returns every /v1 operation keyed the way the registry
// keys routes, so the two can be compared directly.
func swaggerV1Operations(t *testing.T) map[string]struct {
	description string
	projectKey  bool
} {
	t.Helper()

	raw, err := os.ReadFile(swaggerPath)
	if err != nil {
		t.Fatalf("read %s: %v", swaggerPath, err)
	}
	var spec swaggerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse %s: %v", swaggerPath, err)
	}

	out := map[string]struct {
		description string
		projectKey  bool
	}{}

	for path, methods := range spec.Paths {
		if !strings.HasPrefix(path, "/v1/") && path != "/v1" {
			continue
		}
		for method, op := range methods {
			hasKey := false
			for _, scheme := range op.Security {
				if _, ok := scheme["ProjectKeyAuth"]; ok {
					hasKey = true
				}
			}
			out[routeKey(strings.ToUpper(method), swaggerToGinPath(path))] = struct {
				description string
				projectKey  bool
			}{op.Description, hasKey}
		}
	}

	if len(out) == 0 {
		t.Fatal("no /v1 operations found in the Swagger document; the comparison would pass vacuously")
	}
	return out
}

// swaggerToGinPath rewrites `{workspace_id}` into `:workspace_id`. The two
// notations describe the same route and neither side is going to change, so the
// translation lives here rather than in either source of truth.
func swaggerToGinPath(p string) string {
	p = strings.ReplaceAll(p, "{", ":")
	return strings.ReplaceAll(p, "}", "")
}

// TestOpenAPI_ProjectKeyAuthMatchesTheRegistry is the drift gate.
func TestOpenAPI_ProjectKeyAuthMatchesTheRegistry(t *testing.T) {
	ops := swaggerV1Operations(t)

	var (
		falselyOffered []string
		wronglyHidden  []string
		undocumented   []string
	)

	for _, key := range RegisteredRoutes() {
		op, documented := ops[key]
		if !documented {
			undocumented = append(undocumented, key)
			continue
		}
		method, path, _ := strings.Cut(key, " ")
		req, _ := RequirementFor(method, path)

		switch {
		case req.OperatorOnly && op.projectKey:
			falselyOffered = append(falselyOffered, key)
		case !req.OperatorOnly && !op.projectKey:
			wronglyHidden = append(wronglyHidden, key)
		}
	}

	sort.Strings(falselyOffered)
	sort.Strings(wronglyHidden)
	sort.Strings(undocumented)

	for _, key := range falselyOffered {
		t.Errorf("%s declares ProjectKeyAuth but the registry says operator_only.\n"+
			"  The document promises a capability the server refuses; an integration written\n"+
			"  against it fails with 403 after it ships.", key)
	}
	for _, key := range wronglyHidden {
		t.Errorf("%s is reachable by a project credential but the document does not say so.\n"+
			"  A developer reading the spec cannot tell the capability exists.", key)
	}
	for _, key := range undocumented {
		t.Errorf("%s is classified in the registry but absent from the Swagger document.\n"+
			"  Run `make swagger`.", key)
	}
}

// TestOpenAPI_EveryProjectReachableOperationNamesItsScope.
//
// A route a credential may call is useless to a developer who cannot tell which
// scope to grant, and the console mints keys from an explicit scope list. Since
// the format cannot declare the scope, the annotation has to say it — and the
// scope it says must be the scope the registry enforces, not merely some scope.
func TestOpenAPI_EveryProjectReachableOperationNamesItsScope(t *testing.T) {
	ops := swaggerV1Operations(t)

	for _, key := range RegisteredRoutes() {
		method, path, _ := strings.Cut(key, " ")
		req, _ := RequirementFor(method, path)
		if req.OperatorOnly {
			continue
		}
		op, ok := ops[key]
		if !ok {
			continue // reported by the drift gate above
		}

		if !strings.Contains(op.description, "Required scope") {
			t.Errorf("%s does not state a required scope in its description", key)
			continue
		}
		if !strings.Contains(op.description, string(req.Scope)) {
			t.Errorf("%s states a scope the registry does not enforce.\n"+
				"  registry requires %q; the description does not mention it.",
				key, req.Scope)
		}
	}
}

// TestOpenAPI_OperatorOnlyOperationsSayWhy.
//
// `operator_only` is the one refusal a developer cannot fix by adding a scope,
// and the whole point of giving it a code of its own is to stop them looking
// for one. The document has to carry that too, or the code's usefulness stops
// at the runtime response.
func TestOpenAPI_OperatorOnlyOperationsSayWhy(t *testing.T) {
	ops := swaggerV1Operations(t)

	// Not every operator-only route needs the prose — the security scheme's
	// absence already says "no credential here". What must not happen is a
	// route claiming a scope would help.
	for _, key := range RegisteredRoutes() {
		method, path, _ := strings.Cut(key, " ")
		req, _ := RequirementFor(method, path)
		if !req.OperatorOnly {
			continue
		}
		op, ok := ops[key]
		if !ok {
			continue
		}
		if strings.Contains(op.description, "Required scope") {
			t.Errorf("%s is operator-only but its description states a required scope, "+
				"which tells a developer to go looking for a key that cannot exist", key)
		}
	}
}

// TestOpenAPI_ProjectKeyAuthSchemeIsDescribed — the scheme itself has to explain
// the token format and the two things that are not obvious from any single
// operation: the workspace binding, and that control-plane routes are closed.
func TestOpenAPI_ProjectKeyAuthSchemeIsDescribed(t *testing.T) {
	raw, err := os.ReadFile(swaggerPath)
	if err != nil {
		t.Fatalf("read %s: %v", swaggerPath, err)
	}
	var spec swaggerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse: %v", err)
	}

	scheme, ok := spec.SecurityDefinitions["ProjectKeyAuth"]
	if !ok {
		t.Fatal("ProjectKeyAuth is not declared in securityDefinitions")
	}
	for _, want := range []string{"lw_sk_", "workspace", "operator_only"} {
		if !strings.Contains(scheme.Description, want) {
			t.Errorf("the ProjectKeyAuth description does not mention %q", want)
		}
	}
}
