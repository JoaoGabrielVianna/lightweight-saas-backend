package lightweight_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The SDK half of the capability-completeness gate.
//
// apicoverage.json claims that each Project-accessible route is either served by
// a named SDK method or consciously omitted with a reason. The server-side half
// (internal/authz/sdk_coverage_test.go) checks the claims against the
// authorization registry. This half checks them against the code: that the named
// method exists, is exported, and documents the scope it needs.
//
// Without this side, the manifest would be a file anyone could satisfy by typing
// a plausible method name.

const coverageManifestPath = "apicoverage.json"

type coverageEntry struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
	SDK    string `json:"sdk"`
	Reason string `json:"reason"`
}

type coverageManifest struct {
	Routes []coverageEntry `json:"routes"`
}

func loadCoverageManifest(t *testing.T) coverageManifest {
	t.Helper()

	raw, err := os.ReadFile(coverageManifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", coverageManifestPath, err)
	}
	var m coverageManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", coverageManifestPath, err)
	}
	if len(m.Routes) == 0 {
		t.Fatalf("%s declares no routes; every check here would pass vacuously", coverageManifestPath)
	}
	return m
}

// exportedMethod is one method this package exports, with its doc comment.
type exportedMethod struct {
	receiver string
	name     string
	doc      string
}

// exportedMethods parses the package and indexes methods by "Receiver.Name".
func exportedMethods(t *testing.T) map[string]exportedMethod {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	out := map[string]exportedMethod{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || !fn.Name.IsExported() {
					continue
				}
				recv := receiverTypeName(fn.Recv.List[0].Type)
				if recv == "" {
					continue
				}
				m := exportedMethod{receiver: recv, name: fn.Name.Name}
				if fn.Doc != nil {
					m.doc = fn.Doc.Text()
				}
				out[recv+"."+fn.Name.Name] = m
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no exported methods found; the comparison would pass vacuously")
	}
	return out
}

// receiverTypeName unwraps `*T` and `T` to `T`.
func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

// TestCoverage_SupportedEntriesNameRealMethods.
//
// A manifest entry that names a method which does not exist is worse than no
// entry: it reports coverage the package does not have, and it does so to the
// gate that exists to catch exactly that.
func TestCoverage_SupportedEntriesNameRealMethods(t *testing.T) {
	manifest := loadCoverageManifest(t)
	methods := exportedMethods(t)

	for _, entry := range manifest.Routes {
		switch entry.Status {
		case "supported":
			if entry.SDK == "" {
				t.Errorf("%s %s is marked supported but names no SDK method", entry.Method, entry.Path)
				continue
			}
			if _, ok := methods[entry.SDK]; !ok {
				t.Errorf("%s %s claims to be served by %s, which this package does not export",
					entry.Method, entry.Path, entry.SDK)
			}
		case "unsupported":
			if strings.TrimSpace(entry.Reason) == "" {
				t.Errorf("%s %s is marked unsupported with no reason.\n\n"+
					"An omission somebody argued for is a decision; an omission nobody wrote\n"+
					"down is an oversight. The gate cannot tell them apart, so the reason is\n"+
					"required.", entry.Method, entry.Path)
			}
			if entry.SDK != "" {
				t.Errorf("%s %s is marked unsupported but names the SDK method %s",
					entry.Method, entry.Path, entry.SDK)
			}
		default:
			t.Errorf("%s %s has status %q; want \"supported\" or \"unsupported\"",
				entry.Method, entry.Path, entry.Status)
		}
	}
}

// TestCoverage_SupportedMethodsDocumentTheirScope.
//
// A method a credential may call is useless to a developer who cannot tell which
// scope to ask their operator for. The scope stated must be the one the registry
// enforces — which the manifest carries, and which the server-side half of this
// gate pins against the registry itself.
func TestCoverage_SupportedMethodsDocumentTheirScope(t *testing.T) {
	manifest := loadCoverageManifest(t)
	methods := exportedMethods(t)

	for _, entry := range manifest.Routes {
		if entry.Status != "supported" {
			continue
		}
		m, ok := methods[entry.SDK]
		if !ok {
			continue // already reported above
		}
		if !strings.Contains(m.doc, "Required scope") {
			t.Errorf("%s has no \"Required scope:\" line in its documentation", entry.SDK)
			continue
		}
		if !strings.Contains(m.doc, entry.Scope) {
			t.Errorf("%s documents a scope other than the one the server enforces.\n"+
				"  the route requires %q, and the doc comment does not mention it.",
				entry.SDK, entry.Scope)
		}
	}
}

// TestCoverage_NoDuplicateRoutes — two entries for one route would let a stale
// classification hide behind a fresh one, and the server-side gate only checks
// that each route appears at least once.
func TestCoverage_NoDuplicateRoutes(t *testing.T) {
	manifest := loadCoverageManifest(t)

	seen := map[string]bool{}
	for _, entry := range manifest.Routes {
		key := entry.Method + " " + entry.Path
		if seen[key] {
			t.Errorf("%s appears more than once in %s", key, coverageManifestPath)
		}
		seen[key] = true
	}
}

// TestCoverage_EveryServiceMethodIsInTheManifest is the reverse direction.
//
// The server-side gate catches a route with no SDK decision. This one catches an
// SDK method that reaches a route nobody classified — a method added against an
// endpoint that is not in the manifest, and therefore not checked against the
// registry or the OpenAPI document by anything.
func TestCoverage_EveryServiceMethodIsInTheManifest(t *testing.T) {
	manifest := loadCoverageManifest(t)
	methods := exportedMethods(t)

	claimed := map[string]bool{}
	for _, entry := range manifest.Routes {
		if entry.SDK != "" {
			claimed[entry.SDK] = true
		}
	}

	// Methods on a *Service type are the ones that make requests. Everything
	// else (String, GoString, HasMore, ExpiresAtTime, accessors) is local.
	//
	// Audit.All is exempt: it is a wrapper over Audit.List and issues no request
	// of its own, so classifying it separately would report two SDK methods for
	// one route.
	exempt := map[string]bool{"AuditService.All": true}

	for key, m := range methods {
		if !strings.HasSuffix(m.receiver, "Service") || exempt[key] {
			continue
		}
		if !claimed[key] {
			t.Errorf("%s is an exported service method with no entry in %s.\n\n"+
				"Every request-making method must be classified, or the route it targets is\n"+
				"checked against neither the authorization registry nor the OpenAPI document.",
				key, coverageManifestPath)
		}
	}
}
