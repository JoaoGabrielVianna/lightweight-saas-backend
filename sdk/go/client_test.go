package lightweight_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// TestNewClient_RejectsUnusableConfigurationEagerly.
//
// Every one of these fails at startup rather than as a confusing runtime error
// on the first call — which, for a backend, means "at deploy" rather than "the
// first time a customer signs up". The distinction is the point of validating at
// all: a misconfigured process that starts is a process that will fail later, in
// production, under load, with a message about DNS.
func TestNewClient_RejectsUnusableConfigurationEagerly(t *testing.T) {
	valid := lightweight.Config{
		BaseURL:     "https://identity.example.test",
		WorkspaceID: testWorkspace,
		APIKey:      testAPIKey,
	}

	tests := []struct {
		name   string
		mutate func(*lightweight.Config)
		want   string
	}{
		{"empty base url", func(c *lightweight.Config) { c.BaseURL = "" }, "BaseURL is empty"},
		{"base url with no scheme", func(c *lightweight.Config) { c.BaseURL = "identity.example.test" }, "no scheme"},
		{"base url with a wrong scheme", func(c *lightweight.Config) { c.BaseURL = "ftp://identity.example.test" }, "want http or https"},
		{"base url with no host", func(c *lightweight.Config) { c.BaseURL = "https:///v1" }, "no host"},
		{"unparseable base url", func(c *lightweight.Config) { c.BaseURL = "https://exa mple\x7f.test" }, "is not a URL"},
		{"base url carrying a query", func(c *lightweight.Config) { c.BaseURL = "https://identity.example.test?token=x" }, "query or fragment"},

		{"empty workspace", func(c *lightweight.Config) { c.WorkspaceID = "" }, "WorkspaceID is empty"},
		{"workspace that is not an id", func(c *lightweight.Config) { c.WorkspaceID = "my-workspace" }, "not a workspace id"},
		{"workspace with a truncated uuid", func(c *lightweight.Config) { c.WorkspaceID = "ws_3f2504e0-4f89" }, "not a workspace id"},
		{"a project id in the workspace field", func(c *lightweight.Config) {
			c.WorkspaceID = "prj_3f2504e0-4f89-41d3-9a0c-0305e82c3301"
		}, "not a workspace id"},

		{"empty api key", func(c *lightweight.Config) { c.APIKey = "" }, "APIKey is empty"},
		{"an operator bearer token in the key field", func(c *lightweight.Config) {
			c.APIKey = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJvcCJ9.sig"
		}, "not a Project Credential"},
		{"key that is only the prefix", func(c *lightweight.Config) { c.APIKey = "lw_sk_" }, "only the"},
		{"key with trailing whitespace", func(c *lightweight.Config) { c.APIKey = testAPIKey + "\n" }, "whitespace"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)

			client, err := lightweight.NewClient(cfg)
			if err == nil {
				t.Fatalf("NewClient accepted %s and returned %v", tc.name, client)
			}
			if !errors.Is(err, lightweight.ErrConfig) {
				t.Errorf("error does not wrap ErrConfig: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the problem (want it to contain %q)", err, tc.want)
			}
		})
	}
}

// TestNewClient_ConstructionMakesNoNetworkRequest.
//
// Reaching out to validate the credential here would mean a process could not
// start during an unrelated outage of a service it had not yet needed. It also
// makes construction usable in a unit test with no server at all, which is what
// most of this suite relies on.
func TestNewClient_ConstructionMakesNoNetworkRequest(t *testing.T) {
	_, ts := newTestServer(t, jsonResponse(http.StatusOK, `{}`))

	if n := ts.count(); n != 0 {
		t.Errorf("NewClient issued %d request(s); construction must be offline", n)
	}
}

// TestNewClient_NormalisesTheWorkspaceID — a bare UUID is accepted because the
// server accepts one, and normalised so every URL this package builds is spelled
// the same way in an access log.
func TestNewClient_NormalisesTheWorkspaceID(t *testing.T) {
	bare := strings.TrimPrefix(testWorkspace, "ws_")

	for _, given := range []string{testWorkspace, bare, strings.ToUpper(bare)} {
		client, err := lightweight.NewClient(lightweight.Config{
			BaseURL: "https://identity.example.test", WorkspaceID: given, APIKey: testAPIKey,
		})
		if err != nil {
			t.Fatalf("NewClient(%q): %v", given, err)
		}
		if got := client.WorkspaceID(); got != testWorkspace {
			t.Errorf("WorkspaceID() for input %q = %q, want %q", given, got, testWorkspace)
		}
	}
}

// TestNewClient_PreservesAPathPrefix — an installation served under a
// reverse-proxy subpath must work without a second configuration field.
func TestNewClient_PreservesAPathPrefix(t *testing.T) {
	client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"users":[],"first":0,"max":20,"count":0}`))

	prefixed, err := lightweight.NewClient(lightweight.Config{
		BaseURL: ts.URL + "/lightweight/", WorkspaceID: testWorkspace, APIKey: testAPIKey,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client

	if _, err := prefixed.Users.List(testContext(t), lightweight.UserListOptions{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	got := ts.last(t).Path
	want := "/lightweight/v1/workspaces/" + testWorkspace + "/users"
	if got != want {
		t.Errorf("path = %q, want %q (the trailing slash must be normalised, the prefix kept)", got, want)
	}
}

// TestNewClient_DoesNotTouchTheGlobalHTTPClient.
//
// A library that sets http.DefaultClient.Timeout changes the behaviour of every
// other library in the process that reached for the same default — silently, and
// only in programs that happen to import both.
func TestNewClient_DoesNotTouchTheGlobalHTTPClient(t *testing.T) {
	beforeClient := *http.DefaultClient
	beforeTransport := http.DefaultTransport

	client := mustClient(t)
	_ = client

	if http.DefaultClient.Timeout != beforeClient.Timeout {
		t.Errorf("http.DefaultClient.Timeout changed to %v", http.DefaultClient.Timeout)
	}
	if http.DefaultClient.Transport != beforeClient.Transport {
		t.Error("http.DefaultClient.Transport was replaced")
	}
	if http.DefaultTransport != beforeTransport {
		t.Error("http.DefaultTransport was replaced")
	}
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestConfig_HTTPClientIsAuthoritative.
//
// The documented contract is that a caller-supplied client is used verbatim: not
// wrapped, not re-timed, not given a different transport. This is what makes
// tracing, metrics and retry policy possible from outside without this package
// growing a hook system for any of them.
func TestConfig_HTTPClientIsAuthoritative(t *testing.T) {
	var sawRequest bool

	custom := &http.Client{
		Timeout: 7 * time.Second,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			sawRequest = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       http.NoBody,
			}, nil
		}),
	}

	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL: "https://identity.example.test", WorkspaceID: testWorkspace,
		APIKey: testAPIKey, HTTPClient: custom,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// http.NoBody decodes as EOF, which is a protocol error — irrelevant here.
	// What matters is which transport ran and that the client was left alone.
	_, _ = client.Users.List(testContext(t), lightweight.UserListOptions{})

	if !sawRequest {
		t.Error("the caller's RoundTripper was not used")
	}
	if custom.Timeout != 7*time.Second {
		t.Errorf("the caller's Timeout was changed to %v", custom.Timeout)
	}
	if _, ok := custom.Transport.(roundTripperFunc); !ok {
		t.Error("the caller's Transport was replaced")
	}
}

// TestClient_SendsAnIdentifiableUserAgent.
func TestClient_SendsAnIdentifiableUserAgent(t *testing.T) {
	client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"roles":[],"count":0}`))

	if _, err := client.Roles.List(testContext(t)); err != nil {
		t.Fatalf("List: %v", err)
	}
	got := ts.last(t).Header.Get("User-Agent")

	if !strings.HasPrefix(got, "lightweight-go/") {
		t.Errorf("User-Agent = %q, want it to start with lightweight-go/", got)
	}
	if strings.HasSuffix(got, "/") {
		t.Errorf("User-Agent = %q has an empty version segment", got)
	}
	if got != "lightweight-go/"+lightweight.Version() {
		t.Errorf("User-Agent = %q, want it to agree with Version() = %q", got, lightweight.Version())
	}
}

// TestConfig_UserAgentOverrideIsSentVerbatim — a service identifying itself to
// an operator reading access logs gets exactly what it asked for.
func TestConfig_UserAgentOverrideIsSentVerbatim(t *testing.T) {
	_, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"roles":[],"count":0}`))

	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL: ts.URL, WorkspaceID: testWorkspace, APIKey: testAPIKey,
		UserAgent: "billing-worker/2.1 lightweight-go/custom",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Roles.List(testContext(t)); err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := ts.last(t).Header.Get("User-Agent"); got != "billing-worker/2.1 lightweight-go/custom" {
		t.Errorf("User-Agent = %q, want the override verbatim", got)
	}
}

// TestVersion_IsNeverAFabricatedRelease.
//
// A hard-coded version is a lie from the moment it is written: it reports the
// same number from every build, including the one someone is bisecting. In a
// test binary this module IS the main module, so there is no recorded version
// and "dev" is the only honest answer.
func TestVersion_IsNeverAFabricatedRelease(t *testing.T) {
	if got := lightweight.Version(); got != "dev" {
		t.Errorf("Version() = %q in a test binary, where there is no recorded module version; want \"dev\"", got)
	}
}

// TestConfig_RedactsTheAPIKeyInEveryFormattingVerb.
//
// This is the check that makes the "the key never leaks" claim structural rather
// than a matter of everyone remembering. %v, %+v, %s and %#v are the four ways a
// struct reaches a log line by accident, and all four are covered — %#v needing
// GoString specifically, which is the one people forget.
func TestConfig_RedactsTheAPIKeyInEveryFormattingVerb(t *testing.T) {
	cfg := lightweight.Config{
		BaseURL: "https://identity.example.test", WorkspaceID: testWorkspace, APIKey: testAPIKey,
	}

	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		rendered := fmt.Sprintf(verb, cfg)
		if strings.Contains(rendered, testAPIKey) {
			t.Errorf("%s rendered the whole API key", verb)
		}
		if strings.Contains(rendered, "canaryvalue") {
			t.Errorf("%s rendered part of the secret segment: %s", verb, rendered)
		}
		if !strings.Contains(rendered, "lw_sk_") {
			t.Errorf("%s = %q, want it to keep enough to tell two keys apart", verb, rendered)
		}
	}
}

// TestClient_RedactsTheAPIKeyInEveryFormattingVerb — the same property for the
// constructed client, which is the value more likely to be printed.
func TestClient_RedactsTheAPIKeyInEveryFormattingVerb(t *testing.T) {
	client := mustClient(t)

	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		rendered := fmt.Sprintf(verb, client)
		if strings.Contains(rendered, testAPIKey) || strings.Contains(rendered, "canaryvalue") {
			t.Errorf("%s leaked the API key: %s", verb, rendered)
		}
	}
}

// TestCreateUserRequest_RedactsTheTemporaryPassword.
//
// The create-user body carries a password the server never echoes back. This
// side must not undo that: a request struct is exactly the sort of value that
// gets printed while someone works out why creation failed.
func TestCreateUserRequest_RedactsTheTemporaryPassword(t *testing.T) {
	const sentinel = "temporary-password-canary-8891"

	req := lightweight.CreateUserRequest{
		Email:             "ada@example.test",
		TemporaryPassword: sentinel,
		Roles:             []string{"support"},
	}

	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		rendered := fmt.Sprintf(verb, req)
		if strings.Contains(rendered, sentinel) {
			t.Errorf("%s rendered the temporary password: %s", verb, rendered)
		}
		if !strings.Contains(rendered, "ada@example.test") {
			t.Errorf("%s = %q; redaction must not make the value useless for debugging", verb, rendered)
		}
	}
}

// TestNewClientFromEnv_ReadsExactlyTheThreeVariables.
//
// The helper exists to make the integration contract executable. If it ever
// needed a fourth variable, that would be a change to the product's central
// claim, and this test is where it would be noticed.
func TestNewClientFromEnv_ReadsExactlyTheThreeVariables(t *testing.T) {
	t.Setenv(lightweight.EnvBaseURL, "https://identity.example.test")
	t.Setenv(lightweight.EnvWorkspaceID, testWorkspace)
	t.Setenv(lightweight.EnvAPIKey, testAPIKey)

	client, err := lightweight.NewClientFromEnv()
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}
	if client.WorkspaceID() != testWorkspace {
		t.Errorf("WorkspaceID() = %q", client.WorkspaceID())
	}
	if client.BaseURL() != "https://identity.example.test" {
		t.Errorf("BaseURL() = %q", client.BaseURL())
	}

	t.Setenv(lightweight.EnvAPIKey, "")
	if _, err := lightweight.NewClientFromEnv(); err == nil {
		t.Fatal("NewClientFromEnv succeeded with no API key")
	} else if !strings.Contains(err.Error(), lightweight.EnvAPIKey) {
		t.Errorf("the error does not name the missing variable: %v", err)
	}
}
