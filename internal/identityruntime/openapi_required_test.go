package identityruntime

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Required fields: the runtime and the document must agree.
//
// Slice 8's external probe found `temporary_password` required by the service
// and optional in the OpenAPI document. That combination is the worst of both:
// a developer reads the spec, omits the field, and gets a 400 the spec said
// could not happen — and a code generator produces a client whose signature is
// wrong.
//
// Marking the field required fixes the instance. This test fixes the mechanism,
// and it does so without a maintained list being the only thing standing
// between the two, because a maintained list is exactly what drifted.
//
// # The shape of the check
//
//	table  →  runtime   omit the field, assert 400 and that the error NAMES it
//	table  →  document   assert the OpenAPI definition marks it required
//
// The table is verified from both sides, so neither can quietly stop being
// true. An entry that is not actually required fails the runtime half; a field
// that becomes required without the annotation fails the document half.
//
// Scope is deliberately the PROJECT-ACCESSIBLE routes — the machine surface an
// SDK will be generated from. This is not a Swagger audit.

// requiredField is one field the runtime rejects when absent.
type requiredField struct {
	// definition is the OpenAPI definition name.
	definition string
	// field is the JSON key.
	field string
	// method and path are how to provoke the rejection.
	method, path string
	// bodyWithout is a request body that is valid EXCEPT for this field.
	bodyWithout string
}

func requiredFields() []requiredField {
	ws := "/v1/workspaces/" + testPublicID
	return []requiredField{
		{
			definition: "identityruntime.CreateUserRequest", field: "email",
			method: http.MethodPost, path: ws + "/users",
			bodyWithout: `{"temporary_password":"long-enough-1234"}`,
		},
		{
			definition: "identityruntime.CreateUserRequest", field: "temporary_password",
			method: http.MethodPost, path: ws + "/users",
			bodyWithout: `{"email":"ada@example.test"}`,
		},
		{
			definition: "identityruntime.CreateRoleRequest", field: "name",
			method: http.MethodPost, path: ws + "/roles",
			bodyWithout: `{"description":"no name"}`,
		},
		{
			definition: "identityruntime.CreateInvitationRequest", field: "email",
			method: http.MethodPost, path: ws + "/invitations",
			bodyWithout: `{"roles":["support"]}`,
		},
		{
			definition: "identityruntime.CreateInvitationRequest", field: "roles",
			method: http.MethodPost, path: ws + "/invitations",
			bodyWithout: `{"email":"ada@example.test"}`,
		},
		{
			definition: "identityruntime.AssignRolesRequest", field: "roles",
			method: http.MethodPost, path: ws + "/users/" + fieldTestUserID + "/roles",
			bodyWithout: `{}`,
		},
		{
			// Operator-only rather than project-accessible, so outside the
			// stated scope — but the annotation exists, and the reverse gate
			// below requires every annotation to be backed by a proof. An
			// annotation nobody verifies is the thing that drifted.
			definition: "identityruntime.SetPasswordRequest", field: "password",
			method: http.MethodPut, path: ws + "/users/" + fieldTestUserID + "/password",
			bodyWithout: `{"temporary":false}`,
		},
	}
}

// TestOpenAPIRequired_TheRuntimeActuallyRejectsEachOne — the table → runtime
// half. Without it, someone could silence the document half by marking a field
// required that is not, and the spec would start lying in the other direction.
func TestOpenAPIRequired_TheRuntimeActuallyRejectsEachOne(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	for _, rf := range requiredFields() {
		t.Run(rf.definition+"."+rf.field, func(t *testing.T) {
			w := do(r, rf.method, rf.path, rf.bodyWithout)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("omitting %s returned %d, want 400 — it is documented as required "+
					"but the runtime accepts it missing (body %s)", rf.field, w.Code, w.Body.String())
			}
			body := decodeError(t, w)
			if body.Field != rf.field {
				t.Errorf("omitting %s produced field %q; the error must name the field a "+
					"client has to add", rf.field, body.Field)
			}
		})
	}
}

// TestOpenAPIRequired_TheDocumentMarksEachOne — the table → document half.
//
// This is the one that would have caught the shipped defect.
func TestOpenAPIRequired_TheDocumentMarksEachOne(t *testing.T) {
	definitions := swaggerDefinitions(t)

	for _, rf := range requiredFields() {
		def, ok := definitions[rf.definition]
		if !ok {
			t.Errorf("%s is not in the OpenAPI document; run `make swagger`", rf.definition)
			continue
		}
		if !declaresRequired(def.Required, rf.field) {
			t.Errorf("%s.%s is required by the runtime but the OpenAPI document does not "+
				"mark it required.\n"+
				"  A developer reading the spec omits it and gets a 400 the spec said could\n"+
				"  not happen, and a generated client has the wrong signature.\n"+
				"  Add `validate:\"required\"` to the struct tag and run `make swagger`.\n"+
				"  Currently required: %v", rf.definition, rf.field, def.Required)
		}
	}
}

// TestOpenAPIRequired_NothingIsMarkedRequiredThatIsNot — the reverse drift.
//
// A field marked required that the runtime accepts as absent makes a generated
// client demand something the API does not need, which is how a capability
// disappears from an SDK without anyone deciding to remove it.
func TestOpenAPIRequired_NothingIsMarkedRequiredThatIsNot(t *testing.T) {
	definitions := swaggerDefinitions(t)

	// Every identityruntime request definition, and what the table says.
	declared := map[string][]string{}
	for name, def := range definitions {
		if !strings.HasPrefix(name, "identityruntime.") || !strings.HasSuffix(name, "Request") {
			continue
		}
		if len(def.Required) > 0 {
			declared[name] = def.Required
		}
	}
	if len(declared) == 0 {
		t.Fatal("no identityruntime request definition marks any field required; " +
			"either the annotations were lost or this gate is asleep")
	}

	known := map[string]bool{}
	for _, rf := range requiredFields() {
		known[rf.definition+"."+rf.field] = true
	}

	for definition, fields := range declared {
		for _, field := range fields {
			if !known[definition+"."+field] {
				t.Errorf("%s marks %q required, but no entry in requiredFields() proves the "+
					"runtime rejects it.\n"+
					"  Either add the entry (and let it be verified against the runtime), or "+
					"drop the annotation.", definition, field)
			}
		}
	}
}

// swaggerDefinition is the slice of an OpenAPI definition this test reads.
type swaggerDefinition struct {
	Required []string `json:"required"`
}

func swaggerDefinitions(t *testing.T) map[string]swaggerDefinition {
	t.Helper()

	raw, err := os.ReadFile("../../docs/swagger.json")
	if err != nil {
		t.Fatalf("read the OpenAPI document: %v", err)
	}
	var spec struct {
		Definitions map[string]swaggerDefinition `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse the OpenAPI document: %v", err)
	}
	if len(spec.Definitions) == 0 {
		t.Fatal("the OpenAPI document has no definitions")
	}
	return spec.Definitions
}

// declaresRequired reports whether an OpenAPI definition's `required` list
// contains a field. A linear scan: the generator does not promise sorted
// output, and these lists hold at most a handful of names.
func declaresRequired(required []string, field string) bool {
	for _, name := range required {
		if name == field {
			return true
		}
	}
	return false
}
