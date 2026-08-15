package lightweight_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// The unit suite runs against httptest, and the acceptance suite (build tag
// `acceptance`) runs against a real LIGHTWEIGHT, PostgreSQL and identity
// provider. Both are necessary and neither replaces the other:
//
//	httptest      exact request shape, header presence, error decoding,
//	              malformed and hostile responses, cancellation, body limits.
//	              None of these can be provoked reliably against a real server.
//	real stack    that the shapes asserted here are the shapes the server
//	              actually produces. A fixture is only ever as true as the day
//	              it was written.
//
// Everything here is in package lightweight_test, so the suite exercises exactly
// the surface a consumer has. A test that reached into an unexported field could
// pass while the public API was unusable.

// testWorkspace is a syntactically valid workspace id used throughout.
const testWorkspace = "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"

// testAPIKey is a UNIQUE SENTINEL, not a plausible-looking key.
//
// The secret-hygiene tests scan errors and formatted output for this exact
// string, so it must be a value that could not appear by coincidence. It has the
// real prefix so it passes construction, and a body that is obviously a marker.
const testAPIKey = "lw_sk_zzzzsentinelzzz_" +
	"canaryvaluenevertoappearinanyoutputwhatsoeverxx234567"

// capturedRequest is what the fake server saw.
type capturedRequest struct {
	Method string
	// Path is the DECODED path, which is what a handler sees.
	Path string
	// RawPath is the path exactly as it went over the wire. The two differ
	// whenever a segment was escaped, which is precisely the case the
	// path-escaping test is about — asserting on Path there would assert that
	// the server decoded correctly rather than that the client encoded at all.
	RawPath string
	Query   string
	Header  http.Header
	Body    string
}

// testServer is an httptest.Server that records requests and replies with a
// caller-supplied handler.
type testServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []capturedRequest
}

// newTestServer starts a recording server and returns a client pointed at it.
//
// The handler receives the request as usual; the recording happens first, so a
// handler that panics still leaves evidence of what was asked.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*lightweight.Client, *testServer) {
	t.Helper()

	ts := &testServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		ts.mu.Lock()
		ts.requests = append(ts.requests, capturedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			RawPath: r.URL.EscapedPath(),
			Query:   r.URL.RawQuery,
			Header:  r.Header.Clone(),
			Body:    string(body),
		})
		ts.mu.Unlock()

		handler(w, r)
	}))
	t.Cleanup(ts.Close)

	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     ts.URL,
		WorkspaceID: testWorkspace,
		APIKey:      testAPIKey,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, ts
}

// last returns the most recent request, failing if there was none.
func (ts *testServer) last(t *testing.T) capturedRequest {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.requests) == 0 {
		t.Fatal("the client made no request")
	}
	return ts.requests[len(ts.requests)-1]
}

// count returns how many requests were seen.
func (ts *testServer) count() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.requests)
}

// jsonResponse writes a status and body, with the request id header every /v1
// response carries.
func jsonResponse(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", testRequestID)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// testRequestID is the correlation id the fake server echoes.
const testRequestID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

// errorEnvelope renders the /v1 error contract.
func errorEnvelope(code, message string) string {
	return `{"error":{"code":"` + code + `","message":"` + message +
		`","request_id":"` + testRequestID + `"}}`
}

// testContext returns a context cancelled when the test finishes.
//
// It is what testing.T.Context would give, written out because this module's go
// directive is 1.23: the SDK's minimum is chosen for the CONSUMERS who have to
// adopt it, not for the convenience of its own test suite, and reaching for a
// 1.24 helper here would quietly raise the floor for everybody.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// mustClient builds a client against a URL that is never dialled, for tests
// about construction and formatting rather than traffic.
func mustClient(t *testing.T) *lightweight.Client {
	t.Helper()
	c, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     "https://identity.example.test",
		WorkspaceID: testWorkspace,
		APIKey:      testAPIKey,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}
