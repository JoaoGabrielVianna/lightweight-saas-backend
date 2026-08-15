package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// modulePath is this module's import path. Anything under it is "inside".
const modulePath = "github.com/JoaoGabrielVianna/lightweight-saas-backend"

// TestLwprobe_ImportsNothingInternal is the load-bearing test of this program.
//
// lwprobe's whole value is that it is restricted to the public HTTP contract. A
// single import of internal/project or internal/auth would let it construct a
// principal, hash a token or read the database, and every claim it makes about
// "an external backend can do this" would quietly become a claim about "this
// module can do this to itself".
//
// Being in the same repository, that import is one autocomplete away, and a
// reviewer would have no reason to notice it. So the restriction is a test
// rather than a convention: it reads the actual import declarations, and it
// fails on the first one that points back inside.
//
// The stdlib is fine. Third-party modules would be too, in principle — but
// there are none, and a consumer that needs none is itself part of the evidence
// that a thin SDK is possible.
func TestLwprobe_ImportsNothingInternal(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no source files found; the check would pass vacuously")
	}

	fset := token.NewFileSet()
	checked := 0

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++

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
				t.Fatalf("%s: unquote import %s: %v", name, imp.Path.Value, err)
			}
			if strings.HasPrefix(path, modulePath) {
				t.Errorf("%s imports %s\n\n"+
					"lwprobe must reach LIGHTWEIGHT only over HTTP. Importing anything from\n"+
					"this module lets it use knowledge a real consumer does not have, and\n"+
					"every conclusion the harness reports stops being about the public\n"+
					"contract. Add the capability to the HTTP API instead.", name, path)
			}
		}
	}

	if checked == 0 {
		t.Fatal("only test files found; the check would pass vacuously")
	}
}

// TestClient_HasOnlyTheThreeContractFields pins the configuration claim.
//
// The slice's architectural criterion is that a consuming backend needs a URL, a
// workspace id and an API key — and specifically that it never needs a Keycloak
// URL, a realm, a client id, a client secret or a connection id. That claim is
// only as good as the client type: if a field for any of those could be added
// without anything failing, the claim would decay silently the first time
// someone found it convenient.
func TestClient_HasOnlyTheThreeContractFields(t *testing.T) {
	c := NewClient("http://localhost:1", "ws_x", "lw_sk_x")

	// Named explicitly rather than reflected over, so adding a field is a
	// compile-time decision someone has to make on purpose.
	if c.BaseURL == "" || c.WorkspaceID == "" || c.APIKey == "" {
		t.Fatal("the three contract fields are not all populated")
	}

	forbidden := []string{
		"KeycloakURL", "Realm", "ClientID", "ClientSecret", "ConnectionID",
		"AdminToken", "ProviderURL", "JWKSURL",
	}
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	decl, _, found := strings.Cut(string(src), "\n}\n")
	if !found {
		t.Fatal("could not isolate the Client declaration")
	}
	for _, field := range forbidden {
		if strings.Contains(decl, field+" ") {
			t.Errorf("Client grew a %s field — a consumer must not need provider configuration", field)
		}
	}
}

// TestSafePrefix_NeverRendersAWholeKey — the harness prints which key it is
// using. That line ends up in CI output, in a terminal scrollback and in a
// pasted bug report, so it must not be enough to authenticate with.
func TestSafePrefix_NeverRendersAWholeKey(t *testing.T) {
	key := "lw_sk_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52)

	got := safePrefix(key)
	if strings.Contains(key, got) && len(got) >= len(key) {
		t.Fatal("safePrefix returned the whole key")
	}
	if len(got) > 20 {
		t.Errorf("safePrefix returned %d characters; that is more identification than a log line needs", len(got))
	}
	if strings.Contains(got, strings.Repeat("b", 8)) {
		t.Error("safePrefix leaked part of the secret segment")
	}
}

// TestLookupSegment pins the helper the secret-hygiene check depends on. A
// broken extractor would make that check pass by looking for the wrong string.
func TestLookupSegment(t *testing.T) {
	lookup := strings.Repeat("a", 16)
	key := "lw_sk_" + lookup + "_" + strings.Repeat("b", 52)

	if got := lookupSegment(key); got != lookup {
		t.Errorf("lookupSegment = %q, want %q", got, lookup)
	}
	if got := lookupSegment("eyJhbGciOi..."); got != "" {
		t.Errorf("lookupSegment on a JWT = %q, want empty", got)
	}
}
