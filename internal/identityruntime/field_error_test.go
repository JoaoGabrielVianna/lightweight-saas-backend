package identityruntime

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TD-029: an `invalid_request` that names the offending field.
//
// The contract has four parts and each has a way of going wrong:
//
//	present when useful    a validation failure names its field
//	absent otherwise       an authorization refusal names none, and does not
//	                       ship an empty key that a client could misread
//	server-controlled      the name is never derived from client input
//	real                   the name matches a field the client actually sent

// fieldTestUserID is any valid UUID: these tests never reach the provider,
// they fail in decoding or validation first.
const fieldTestUserID = "9c1e6679-7425-40de-944b-e07fc1f90ae7"

// TestFieldError_ValidationNamesTheField walks the identity service's own
// validation through the handler and asserts the field arrives.
func TestFieldError_ValidationNamesTheField(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	cases := []struct {
		name      string
		method    string
		path      string
		body      string
		wantField string
	}{
		{
			name:   "create a user with no temporary password",
			method: http.MethodPost, path: "/v1/workspaces/" + testPublicID + "/users",
			body:      `{"email":"ada@example.test","first_name":"Ada"}`,
			wantField: "temporary_password",
		},
		{
			name:   "create a user with no email",
			method: http.MethodPost, path: "/v1/workspaces/" + testPublicID + "/users",
			body:      `{"temporary_password":"long-enough-1234"}`,
			wantField: "email",
		},
		{
			name:   "create a user with a malformed email",
			method: http.MethodPost, path: "/v1/workspaces/" + testPublicID + "/users",
			body:      `{"email":"not-an-email","temporary_password":"long-enough-1234"}`,
			wantField: "email",
		},
		{
			name:   "create a user with too short a password",
			method: http.MethodPost, path: "/v1/workspaces/" + testPublicID + "/users",
			body:      `{"email":"ada@example.test","temporary_password":"short"}`,
			wantField: "temporary_password",
		},
		{
			name:   "create a role with no name",
			method: http.MethodPost, path: "/v1/workspaces/" + testPublicID + "/roles",
			body:      `{"description":"no name"}`,
			wantField: "name",
		},
		{
			name:   "invite with no roles",
			method: http.MethodPost, path: "/v1/workspaces/" + testPublicID + "/invitations",
			body:      `{"email":"ada@example.test","roles":[]}`,
			wantField: "roles",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(r, tc.method, tc.path, tc.body)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			body := decodeError(t, w)
			if body.Code != "invalid_request" {
				t.Errorf("code = %q, want invalid_request", body.Code)
			}
			if body.Field != tc.wantField {
				t.Errorf("field = %q, want %q — a client cannot tell which field to fix",
					body.Field, tc.wantField)
			}
		})
	}
}

// TestFieldError_TypeMismatchNamesTheField — the most common integration
// mistake is a string where a bool belongs, and the decoder already knows which
// key it was.
func TestFieldError_TypeMismatchNamesTheField(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	w := do(r, http.MethodPatch, "/v1/workspaces/"+testPublicID+"/users/"+fieldTestUserID,
		`{"enabled":"yes"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if got := decodeError(t, w).Field; got != "enabled" {
		t.Errorf("field = %q, want \"enabled\"", got)
	}
}

// TestFieldError_AbsentWhenTheErrorIsNotAboutAField.
//
// `omitempty` is the compatibility contract, and it is also a correctness one:
// an error with an empty `field` key invites `if err.Field == ""` to be read as
// "no field" in one place and "the field is blank" in another.
func TestFieldError_AbsentWhenTheErrorIsNotAboutAField(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"a malformed workspace id", http.MethodGet, "/v1/workspaces/not-a-workspace/users", ""},
		{"a malformed user id", http.MethodGet, "/v1/workspaces/" + testPublicID + "/users/nope", ""},
		{"an unparseable body", http.MethodPost, "/v1/workspaces/" + testPublicID + "/users", `{not json`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(r, tc.method, tc.path, tc.body)
			if w.Code < 400 {
				t.Fatalf("status = %d, want an error", w.Code)
			}

			// Assert on the RAW JSON: decoding into a struct cannot tell an
			// absent key from an empty one, which is the whole property here.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatalf("response is not JSON: %s", w.Body.String())
			}
			var errBody map[string]json.RawMessage
			if err := json.Unmarshal(raw["error"], &errBody); err != nil {
				t.Fatalf("error is not an object: %s", w.Body.String())
			}
			if _, present := errBody["field"]; present {
				t.Errorf("the response carries a \"field\" key for an error that is not "+
					"about a field: %s", w.Body.String())
			}
		})
	}
}

// TestFieldError_NeverEchoesClientInput.
//
// The name must be one of ours. A client that sends a hostile key must not see
// it reflected: an error body reaches logs, dashboards and support tickets.
func TestFieldError_NeverEchoesClientInput(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	hostile := `<script>alert(1)</script>`
	body := `{"email":"ada@example.test","temporary_password":"short","` + hostile + `":"x"}`

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/users", body)
	if strings.Contains(w.Body.String(), "script") {
		t.Errorf("the response echoed a client-supplied key: %s", w.Body.String())
	}
	if got := decodeError(t, w).Field; got != "temporary_password" {
		t.Errorf("field = %q, want the real offending field", got)
	}
}

// TestErrors_WithFieldRejectsAnythingUnlikeAFieldName — the boundary check.
//
// No caller passes input today. This asserts the guard holds if one ever does,
// which is the only reason to have written it.
func TestErrors_WithFieldRejectsAnythingUnlikeAFieldName(t *testing.T) {
	rejected := []string{
		"", "Email", "e mail", "email;drop", "<script>", "email.sub",
		strings.Repeat("a", 41), "1email", "email-address",
	}
	for _, bad := range rejected {
		if got := ErrInvalidRequest.WithField(bad); got.Field != "" {
			t.Errorf("WithField(%q) produced field %q; it must be dropped", bad, got.Field)
		}
	}

	for _, good := range []string{"email", "temporary_password", "expires_at", "roles", "name"} {
		if got := ErrInvalidRequest.WithField(good); got.Field != good {
			t.Errorf("WithField(%q) produced %q", good, got.Field)
		}
	}
}

// TestErrors_WithFieldDoesNotMutateTheCatalogue.
//
// The catalogue entries are package-level singletons. Mutating one would make
// the NEXT request inherit this request's field — a bug that only appears under
// concurrency and is very hard to read back from a log.
func TestErrors_WithFieldDoesNotMutateTheCatalogue(t *testing.T) {
	before := ErrInvalidRequest.Field

	withField := ErrInvalidRequest.WithField("email")
	if withField == ErrInvalidRequest {
		t.Fatal("WithField returned the singleton itself rather than a copy")
	}
	if ErrInvalidRequest.Field != before {
		t.Errorf("WithField mutated the shared catalogue entry: field is now %q",
			ErrInvalidRequest.Field)
	}
	// Everything else must survive the copy.
	if withField.Code != ErrInvalidRequest.Code || withField.Status != ErrInvalidRequest.Status {
		t.Error("WithField changed the code or status")
	}
}

// TestErrors_FieldNamesMatchTheRequestDTOs is the strong version of the
// "server-controlled names" rule.
//
// `isKnownFieldName` checks the SHAPE of a name. This checks the SUBSTANCE:
// every field name the identity service can produce has to correspond to a real
// JSON key on a request DTO, because a name that matches nothing the client
// sent is worse than no name — it sends them looking for a field that does not
// exist.
//
// It reads the service source rather than a maintained list, so a new
// invalidField call with a typo fails here instead of shipping.
func TestErrors_FieldNamesMatchTheRequestDTOs(t *testing.T) {
	src, err := os.ReadFile("../identity/service.go")
	if err != nil {
		t.Fatalf("read the identity service: %v", err)
	}

	call := regexp.MustCompile(`invalidField\("([a-z_]+)"`)
	matches := call.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("found no invalidField calls; the pattern is wrong and this gate is asleep")
	}

	known := requestFieldNames()
	for _, m := range matches {
		if !known[m[1]] {
			t.Errorf("identity.Service reports the field %q, which is not a JSON key on any "+
				"request DTO in this package.\n  A client told to fix %q cannot find it.\n"+
				"  Known fields: %v", m[1], m[1], sortedKeys(known))
		}
	}
}

// requestFieldNames collects the json tags of every request DTO, which is the
// set of names a client could possibly have sent.
func requestFieldNames() map[string]bool {
	out := map[string]bool{}
	for _, dto := range []any{
		CreateUserRequest{}, UpdateUserRequest{}, CreateRoleRequest{},
		UpdateRoleRequest{}, AssignRolesRequest{}, CreateInvitationRequest{},
		SetPasswordRequest{},
	} {
		t := reflect.TypeOf(dto)
		for i := 0; i < t.NumField(); i++ {
			tag := t.Field(i).Tag.Get("json")
			if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
				out[name] = true
			}
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
