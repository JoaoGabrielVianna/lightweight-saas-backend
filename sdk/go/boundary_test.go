package lightweight_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// serverModulePath is the module this SDK is published from. Anything under it
// is "inside the server".
const serverModulePath = "github.com/JoaoGabrielVianna/lightweight-saas-backend"

// sdkModulePath is this module. It shares the server's prefix, so the import
// check has to exclude it explicitly rather than matching on the prefix alone.
const sdkModulePath = serverModulePath + "/sdk/go"

// sdkSourceFiles returns the non-test Go files of the SDK package and its
// subpackages.
//
// Test files are excluded on purpose. A test may legitimately do things the
// library must not — but note that in this module even the tests import nothing
// from the server, because there is nothing there they could use.
func sdkSourceFiles(t *testing.T) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no SDK source files found; every check in this file would pass vacuously")
	}
	return files
}

// TestSDK_ImportsNothingFromTheServer is the load-bearing test of this package.
//
// The SDK's entire claim is that it is an external consumer: it speaks the
// public HTTP contract and has no other access. One import of
// internal/identityruntime would let it reach a DTO, one import of
// internal/project would let it mint a token, and every statement this package
// makes about "what an external backend can do" would quietly become a statement
// about "what code inside this repository can do to itself".
//
// Being a separate module already makes that import awkward — it would need a
// require line pointing at the parent — but awkward is not the same as
// impossible, and `go mod tidy` writes require lines without being asked twice.
// So the restriction is a test that reads the actual import declarations.
//
// Third-party imports are refused by the same check for a different reason: a
// client library's dependencies become its users' dependencies, and this one
// promises to have none. The stdlib is unrestricted.
func TestSDK_ImportsNothingFromTheServer(t *testing.T) {
	fset := token.NewFileSet()

	for _, name := range sdkSourceFiles(t) {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		file, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", name, imp.Path.Value, err)
			}

			switch {
			case path == sdkModulePath || strings.HasPrefix(path, sdkModulePath+"/"):
				// This module's own packages. Fine.
			case strings.HasPrefix(path, serverModulePath):
				t.Errorf("%s imports %s\n\n"+
					"The SDK must reach LIGHTWEIGHT only over HTTP. Importing the server lets it\n"+
					"use knowledge a real consumer does not have, and every claim this package\n"+
					"makes about the public contract stops being true. If the SDK needs something\n"+
					"the API does not expose, add it to the API.", name, path)
			case strings.Contains(strings.SplitN(path, "/", 2)[0], "."):
				// A dot in the first path segment means a domain, which means a
				// module rather than the standard library.
				t.Errorf("%s imports the third-party module %s\n\n"+
					"This SDK promises consumers a zero-dependency client. A dependency here\n"+
					"becomes a dependency of every backend that imports the SDK, along with its\n"+
					"own transitive graph and its own vulnerability reports.", name, path)
			}
		}
	}
}

// providerVocabulary is the set of terms that would betray the abstraction.
//
// The product's central claim is that a backend developer never learns which
// identity provider sits behind LIGHTWEIGHT. If any of these ends up in an
// identifier or a string literal here, the SDK has started to model the provider
// — which means the abstraction has a hole in it, and the SDK is the thing
// leaking through.
var providerVocabulary = []string{
	"keycloak",
	"realm",
	"openid",
	"oidc",
	"jwks",
	"client_secret",
	"clientsecret",
	"issuer",
}

// TestSDK_HasNoProviderVocabulary checks identifiers and string literals only.
//
// Comments are deliberately EXEMPT, and that exemption is what makes this check
// worth having rather than brittle. The package documentation has to be able to
// say "you never need to know the provider's URL" — refusing the word in prose
// would ban the sentence that explains the guarantee. What must not exist is a
// field, a function, a parameter or a wire value that models the provider,
// because that is not documentation, that is a dependency.
//
// Parsing without ParseComments is what implements the exemption: comments are
// simply not in the tree being walked.
func TestSDK_HasNoProviderVocabulary(t *testing.T) {
	fset := token.NewFileSet()

	for _, name := range sdkSourceFiles(t) {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		file, err := parser.ParseFile(fset, name, src, 0) // no ParseComments
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			var text, kind string
			switch node := n.(type) {
			case *ast.Ident:
				text, kind = node.Name, "identifier"
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				text, kind = node.Value, "string literal"
			default:
				return true
			}

			lower := strings.ToLower(text)
			for _, term := range providerVocabulary {
				if strings.Contains(lower, term) {
					t.Errorf("%s: %s %s contains %q\n\n"+
						"The SDK must not model the identity provider. A backend developer using\n"+
						"this package is promised they never need to know what is behind\n"+
						"LIGHTWEIGHT, and a %s that names it means the abstraction has a hole.\n"+
						"(Prose in comments is exempt — explaining the guarantee is fine.)",
						fset.Position(n.Pos()), kind, text, term, kind)
				}
			}
			return true
		})
	}
}

// TestConfig_HasOnlyTheContractFields pins the three-variable claim as a type.
//
// The claim is that a consuming backend needs a URL, a workspace id and a
// credential, and specifically that it never needs provider configuration. That
// is only true for as long as there is nowhere to put provider configuration. A
// field added "just for this one deployment" would decay the claim silently, and
// a reviewer has no reason to notice a new struct field.
//
// The forbidden names are checked against the declaration source rather than
// reflected over, so adding one is a decision someone has to defend rather than
// a compile that happens to still pass.
func TestConfig_HasOnlyTheContractFields(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}

	_, after, found := strings.Cut(string(src), "type Config struct {")
	if !found {
		t.Fatal("could not locate the Config declaration")
	}
	decl, _, found := strings.Cut(after, "\n}\n")
	if !found {
		t.Fatal("could not find the end of the Config declaration")
	}

	forbidden := []string{
		"ProviderURL", "ProviderRealm", "TenantID", "ClientID", "ClientSecret",
		"ConnectionID", "AdminToken", "OperatorToken", "JWKSURL", "IssuerURL",
		"DatabaseURL", "DBURL",
	}
	for _, field := range forbidden {
		if strings.Contains(decl, field+" ") {
			t.Errorf("Config grew a %s field.\n\n"+
				"A consumer must not need provider or control-plane configuration. If a\n"+
				"deployment appears to need this, the requirement belongs in the API, not in\n"+
				"every backend's environment.", field)
		}
	}

	// And the three that MUST be there, so the test cannot pass by the struct
	// having been renamed or emptied.
	for _, field := range []string{"BaseURL ", "WorkspaceID ", "APIKey "} {
		if !strings.Contains(decl, field) {
			t.Errorf("Config no longer declares %s; the contract fields are the point of this type", field)
		}
	}
}
