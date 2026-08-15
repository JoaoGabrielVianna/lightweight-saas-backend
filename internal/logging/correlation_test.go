package logging

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

// Operational correlation for a machine request.
//
// The Slice 8 requirement is deliberately narrow: no durable audit, no new
// event model, no retention. Just enough that an operator holding one fact
// about a machine request can find the rest.
//
// Four identifiers make that possible, and they are the four an incident
// actually starts from:
//
//	request_id     the caller reports it; it is on every /v1 response
//	credential_id  what an operator REVOKES — the actionable one
//	project_id     which integration it was
//	workspace_id   which tenant, and therefore which realm
//
// credential_id is the one that is easy to leave out and the one that matters
// most: an event naming only the project tells an operator to revoke something,
// without saying which of that project's ten keys.

const (
	testProjectID    = "prj_7c9e6679-7425-40de-944b-e07fc1f90ae7"
	testCredentialID = "key_9b2f4c1a-1111-4222-8333-444455556666"
	testWorkspaceID  = "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"

	// The plaintext token and its non-secret lookup segment. Neither may
	// appear anywhere in an event, and the principal deliberately does not
	// carry either — this test pins that it stays that way.
	testToken  = "lw_sk_zzzzsecretzzzzz_qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	testLookup = "zzzzsecretzzzzz"
)

// machineRequest builds a gin context carrying a resolved project principal and
// a request id, as the /v1 chain leaves it for a handler.
func machineRequest(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/workspaces/"+testWorkspaceID+"/users", nil)
	c.Request.Header.Set("Authorization", "Bearer "+testToken)
	c.Request.Header.Set(requestid.Header, "req-correlation-1")
	c.Request.Header.Set("User-Agent", "acme-backend/1.4")

	requestid.Middleware()(c)
	auth.StorePrincipal(c, auth.NewProjectPrincipal(&auth.ProjectPrincipal{
		ProjectID:    testProjectID,
		ProjectName:  "Billing worker",
		CredentialID: testCredentialID,
		WorkspaceID:  testWorkspaceID,
		Scopes:       []string{"users:write"},
	}))
	return c
}

// TestCorrelation_MachineMutationCarriesAllFourIdentifiers.
func TestCorrelation_MachineMutationCarriesAllFourIdentifiers(t *testing.T) {
	c := machineRequest(t)

	e := EventFromGin(c, audit.Event{
		Action:    audit.ActionUserCreated,
		Target:    audit.Target{Kind: "user", ID: "u-1"},
		Workspace: testWorkspaceID,
	})

	if e.RequestID != "req-correlation-1" {
		t.Errorf("request_id = %q, want the id the caller was given back", e.RequestID)
	}
	if e.Workspace != testWorkspaceID {
		t.Errorf("workspace = %q, want %q", e.Workspace, testWorkspaceID)
	}
	if e.Actor.Type != audit.ActorProject {
		t.Fatalf("actor type = %q, want %q", e.Actor.Type, audit.ActorProject)
	}
	if e.Actor.ProjectID != testProjectID {
		t.Errorf("actor.project_id = %q, want %q", e.Actor.ProjectID, testProjectID)
	}
	if e.Actor.CredentialID != testCredentialID {
		t.Errorf("actor.credential_id = %q, want %q — an operator needs to know "+
			"WHICH key to revoke, not just which project held one",
			e.Actor.CredentialID, testCredentialID)
	}
}

// TestCorrelation_AProjectNeverLooksLikeAnOperator.
//
// audit.Actor.Subject means "a Keycloak sub", and every consumer reads it that
// way. A project id there would make a machine indistinguishable from a person
// in the one record an investigation relies on.
func TestCorrelation_AProjectNeverLooksLikeAnOperator(t *testing.T) {
	c := machineRequest(t)
	actor := ActorFromGin(c)

	if actor.Subject != "" {
		t.Errorf("actor.subject = %q; a project has no Keycloak subject and must not borrow the field", actor.Subject)
	}
	if actor.Email != "" || actor.Username != "" {
		t.Errorf("actor carries operator fields (email=%q username=%q)", actor.Email, actor.Username)
	}
}

// TestCorrelation_NoSecretMaterialReachesAnEvent.
//
// The Authorization header is on the request the event is built from, so
// "nothing copied it" is a property worth pinning rather than assuming. An
// event ends up in a ring buffer, a log line, and — the day durable audit
// arrives — a database column that outlives the credential.
func TestCorrelation_NoSecretMaterialReachesAnEvent(t *testing.T) {
	c := machineRequest(t)

	e := EventFromGin(c, audit.Event{
		Action: audit.ActionUserCreated,
		Target: audit.Target{Kind: "user", ID: "u-1", Name: "ada@example.test"},
		Extra:  map[string]any{"scopes": []string{"users:write"}},
	})

	rendered := renderEvent(t, e)
	for _, secret := range []string{testToken, testLookup, "Bearer "} {
		if strings.Contains(rendered, secret) {
			t.Errorf("event contains %q:\n%s", secret, rendered)
		}
	}
}

// renderEvent flattens an event into one searchable string.
//
// Field-by-field assertions would only cover the fields someone thought of; a
// flattened render catches a secret that arrives in a field added later,
// which is the case worth protecting against.
func renderEvent(t *testing.T, e audit.Event) string {
	t.Helper()

	var b strings.Builder
	b.WriteString(string(e.Action))
	b.WriteString(e.RequestID)
	b.WriteString(e.Workspace)
	b.WriteString(e.IP)
	b.WriteString(e.UserAgent)
	b.WriteString(e.Reason)
	b.WriteString(string(e.Actor.Type))
	b.WriteString(e.Actor.Subject)
	b.WriteString(e.Actor.Email)
	b.WriteString(e.Actor.Username)
	b.WriteString(e.Actor.ProjectID)
	b.WriteString(e.Actor.CredentialID)
	b.WriteString(e.Target.Kind)
	b.WriteString(e.Target.ID)
	b.WriteString(e.Target.Name)
	for k, v := range e.Extra {
		b.WriteString(k)
		b.WriteString(strings.TrimSpace(strings.Join(stringify(v), " ")))
	}
	return b.String()
}

func stringify(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []string:
		return t
	default:
		return nil
	}
}
