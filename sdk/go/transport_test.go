package lightweight_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// TestTransport_SendsTheCredentialAsABearerTokenAndNowhereElse.
//
// Two claims in one test, because they are two halves of the same rule. The key
// must be present — an SDK that forgot the header would fail every call with a
// 401 that looks like a bad credential — and it must be present in EXACTLY one
// place. A key in a URL is a key in every proxy log, every access log and every
// browser history between here and the server, and it survives rotation in all
// of them.
func TestTransport_SendsTheCredentialAsABearerTokenAndNowhereElse(t *testing.T) {
	client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"users":[],"first":0,"max":20,"count":0}`))

	if _, err := client.Users.List(testContext(t), lightweight.UserListOptions{Search: "ada"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	req := ts.last(t)

	if got := req.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
		t.Errorf("Authorization = %q, want the bearer credential", got)
	}
	if strings.Contains(req.Path, "lw_sk_") || strings.Contains(req.Query, "lw_sk_") {
		t.Errorf("the credential reached the URL: path=%q query=%q", req.Path, req.Query)
	}
	if strings.Contains(req.Header.Get("User-Agent"), "lw_sk_") {
		t.Error("the credential reached the User-Agent")
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", req.Header.Get("Accept"))
	}
}

// TestTransport_AddressesOnlyTheConfiguredWorkspace.
//
// The workspace is fixed at construction and written into the URL in one place,
// so no method can address another tenant. This asserts the path that is
// actually produced, which is the thing a server-side authorization check will
// compare against the credential's binding.
func TestTransport_AddressesOnlyTheConfiguredWorkspace(t *testing.T) {
	client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))

	if err := client.Users.Delete(testContext(t), "9c1e6679-7425-40de-944b-e07fc1f90ae7"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := "/v1/workspaces/" + testWorkspace + "/users/9c1e6679-7425-40de-944b-e07fc1f90ae7"
	if got := ts.last(t).Path; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// TestTransport_EscapesCallerSuppliedPathSegments.
//
// A role name is caller data that becomes a path segment. Unescaped, a name
// containing "../" would address a different route entirely — including, in
// principle, one outside this workspace, which is the boundary the whole product
// is built on.
func TestTransport_EscapesCallerSuppliedPathSegments(t *testing.T) {
	client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"id":"1","name":"x"}`))

	_, _ = client.Roles.Get(testContext(t), "../../../etc/passwd")

	// Asserted on the wire form, not on the server's decoded view: the whole
	// point is that the separators were encoded before they could be read as
	// path structure.
	got := ts.last(t).RawPath
	if strings.Contains(got, "/etc/passwd") {
		t.Errorf("raw path = %q; a role name escaped its segment", got)
	}
	want := "/v1/workspaces/" + testWorkspace + "/roles/..%2F..%2F..%2Fetc%2Fpasswd"
	if got != want {
		t.Errorf("raw path = %q, want %q", got, want)
	}
}

// TestAPIError_CarriesTheWholeContract.
//
// Everything a caller needs to react without parsing a string: the status, the
// stable code, the prose, the field when there is one, and the request id that
// ties the failure to the server's own log line.
func TestAPIError_CarriesTheWholeContract(t *testing.T) {
	body := `{"error":{"code":"invalid_request","message":"Request is invalid",` +
		`"field":"temporary_password","request_id":"` + testRequestID + `"}}`
	client, _ := newTestServer(t, jsonResponse(http.StatusBadRequest, body))

	_, err := client.Users.Create(testContext(t), lightweight.CreateUserRequest{Email: "ada@example.test"})

	var apiErr *lightweight.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *lightweight.APIError: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if apiErr.Code != lightweight.CodeInvalidRequest {
		t.Errorf("Code = %q", apiErr.Code)
	}
	if apiErr.Message != "Request is invalid" {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if apiErr.Field != "temporary_password" {
		t.Errorf("Field = %q", apiErr.Field)
	}
	if apiErr.RequestID != testRequestID {
		t.Errorf("RequestID = %q, want %q", apiErr.RequestID, testRequestID)
	}
	if apiErr.Op != "Users.Create" {
		t.Errorf("Op = %q, want the SDK operation", apiErr.Op)
	}
	if !strings.Contains(apiErr.Error(), testRequestID) {
		t.Errorf("Error() = %q; an error line without the request id cannot be followed up", apiErr.Error())
	}
}

// TestAPIError_TakesTheRequestIDFromTheHeaderWhenTheBodyOmitsIt.
//
// The correlation id is the single most useful thing to carry into a caller's
// own logs, so losing it to a proxy that rewrote one of the two places it
// appears would be a real loss.
func TestAPIError_TakesTheRequestIDFromTheHeaderWhenTheBodyOmitsIt(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusNotFound,
		`{"error":{"code":"user_not_found","message":"User not found in this workspace"}}`))

	_, err := client.Users.Get(testContext(t), "9c1e6679-7425-40de-944b-e07fc1f90ae7")

	var apiErr *lightweight.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T: %v", err, err)
	}
	if apiErr.RequestID != testRequestID {
		t.Errorf("RequestID = %q, want the header's %q", apiErr.RequestID, testRequestID)
	}
}

// TestAPIError_AcceptsAnUnknownErrorCode.
//
// A client older than the server must keep working when the server grows a more
// precise code. Modelling codes as a closed enum would turn that improvement
// into an outage for every deployed consumer.
func TestAPIError_AcceptsAnUnknownErrorCode(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusConflict,
		errorEnvelope("quota_exhausted_for_tier", "This workspace is over its user quota")))

	_, err := client.Users.Create(testContext(t), lightweight.CreateUserRequest{Email: "ada@example.test"})

	var apiErr *lightweight.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("a code this package has no constant for did not decode: %v", err)
	}
	if apiErr.Code != "quota_exhausted_for_tier" {
		t.Errorf("Code = %q, want the unknown code preserved verbatim", apiErr.Code)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
}

// TestAPIError_SurvivesAnUnparseableErrorBody.
//
// A 503 from a load balancer that never reached LIGHTWEIGHT is still a refusal a
// caller must handle, and the status is the actionable part. The empty Code says
// truthfully that no machine-readable reason was given — and the body is NOT
// echoed, because an unknown intermediary's page has no business being pasted
// into a caller's logs by this package.
func TestAPIError_SurvivesAnUnparseableErrorBody(t *testing.T) {
	const page = "<html><head><title>503 Service Unavailable</title></head><body>nginx</body></html>"
	client, _ := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(page))
	})

	_, err := client.Roles.List(testContext(t))

	var apiErr *lightweight.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *APIError: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if apiErr.Code != "" {
		t.Errorf("Code = %q, want empty when the server gave no machine-readable reason", apiErr.Code)
	}
	if strings.Contains(apiErr.Error(), "nginx") || strings.Contains(apiErr.Error(), "<html>") {
		t.Errorf("the intermediary's page was echoed into the error: %q", apiErr.Error())
	}
}

// TestAPIError_BoundsTheErrorBodyItReads.
//
// A broken proxy answering 500 with a very large body must not cost this package
// that much memory per failed request — at exactly the moment it is failing
// fastest.
//
// The bound is asserted through its OBSERVABLE consequence rather than by trying
// to measure an allocation. The server sends a well-formed error envelope whose
// `message` is a megabyte long:
//
//	bounded    only the first 64 KiB is read, the JSON is therefore truncated,
//	           fails to parse, and the refusal falls back to the status text
//	unbounded  the whole megabyte parses, and a megabyte-long message ends up on
//	           an error the caller is about to log
//
// The two are trivially distinguishable, which is what makes this a test rather
// than a comment. A version that only sent junk would pass either way — junk
// fails to parse whether or not it was truncated — and that is precisely the
// hole a mutation run found in the first draft of this test.
func TestAPIError_BoundsTheErrorBodyItReads(t *testing.T) {
	const messageSize = 1 << 20 // 1 MiB, far past the 64 KiB bound

	client, _ := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", testRequestID)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"`))
		chunk := strings.Repeat("A", 32<<10)
		for written := 0; written < messageSize; written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`","request_id":"` + testRequestID + `"}}`))
	})

	_, err := client.Roles.List(testContext(t))

	var apiErr *lightweight.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if len(apiErr.Message) > 4096 {
		t.Errorf("Message is %d bytes; the error body is being read without a bound", len(apiErr.Message))
	}
	if len(apiErr.Error()) > 4096 {
		t.Errorf("the error string is %d bytes", len(apiErr.Error()))
	}
	if strings.Contains(apiErr.Error(), "AAAA") {
		t.Error("the oversized body leaked into the error message")
	}
}

// TestAPIError_HostileNonJSONBodyIsNotEchoed is the other half of the same rule:
// whatever an unknown intermediary answered with, this package does not paste it
// into a caller's logs.
func TestAPIError_HostileNonJSONBodyIsNotEchoed(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("B", 4<<20)))
	})

	_, err := client.Roles.List(testContext(t))

	var apiErr *lightweight.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T: %v", err, err)
	}
	if strings.Contains(apiErr.Error(), "BBBB") {
		t.Error("the intermediary's body was echoed into the error")
	}
	if len(apiErr.Error()) > 4096 {
		t.Errorf("the error string is %d bytes", len(apiErr.Error()))
	}
}

// TestProtocolError_MalformedSuccessBodyIsNotSilentlyZeroValued.
//
// This is the failure mode that produces the worst bugs: a 200 whose body cannot
// be read, decoded into a zero value, and handed back as an empty-but-successful
// result. A caller then concludes the workspace has no users and acts on it.
func TestProtocolError_MalformedSuccessBodyIsNotSilentlyZeroValued(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, `{"users": [ {"id": `))

	page, err := client.Users.List(testContext(t), lightweight.UserListOptions{})
	if err == nil {
		t.Fatalf("malformed JSON was accepted; got page %+v", page)
	}
	if page != nil {
		t.Errorf("a partial page was returned alongside the error: %+v", page)
	}

	var protoErr *lightweight.ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("error is %T, want *ProtocolError: %v", err, err)
	}
	if protoErr.Op != "Users.List" {
		t.Errorf("Op = %q, want the operation that failed", protoErr.Op)
	}
	if !strings.Contains(protoErr.Path, "/users") {
		t.Errorf("Path = %q, want the endpoint", protoErr.Path)
	}
	if protoErr.RequestID != testRequestID {
		t.Errorf("RequestID = %q, want the header's", protoErr.RequestID)
	}

	var apiErr *lightweight.APIError
	if errors.As(err, &apiErr) {
		t.Error("a malformed success was reported as an APIError; a caller would read it as a refusal")
	}
}

// TestProtocolError_BoundsTheSuccessBody.
//
// Same reasoning as the error-body bound, and it matters more here: a success
// body is decoded, so an unbounded read is an unbounded allocation followed by
// an unbounded parse.
func TestProtocolError_BoundsTheSuccessBody(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[`))
		// One valid-looking element repeated past the 8 MiB ceiling.
		element := `{"id":"9c1e6679-7425-40de-944b-e07fc1f90ae7","username":"` + strings.Repeat("u", 4096) + `"},`
		for written := 0; written < (9 << 20); written += len(element) {
			if _, err := w.Write([]byte(element)); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`]}`))
	})

	_, err := client.Users.List(testContext(t), lightweight.UserListOptions{})

	var protoErr *lightweight.ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("an oversized success body did not produce a ProtocolError: %T %v", err, err)
	}
	if !strings.Contains(protoErr.Error(), "exceeds") {
		t.Errorf("the error does not say the body was too large: %v", protoErr)
	}
}

// TestRequestError_IsNotAnAPIError.
//
// The distinction is the difference between "the user does not exist" and "the
// network is down". A caller that cannot tell them apart will delete the wrong
// things or retry the wrong things.
func TestRequestError_IsNotAnAPIError(t *testing.T) {
	// A server that is closed before the call: a connection refusal, which is
	// the most common transport failure in practice.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL: url, WorkspaceID: testWorkspace, APIKey: testAPIKey,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.Users.List(testContext(t), lightweight.UserListOptions{})
	if err == nil {
		t.Fatal("a call against a closed listener succeeded")
	}

	var reqErr *lightweight.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error is %T, want *RequestError: %v", err, err)
	}
	var apiErr *lightweight.APIError
	if errors.As(err, &apiErr) {
		t.Error("a transport failure was reported as an APIError")
	}
	if reqErr.Op != "Users.List" {
		t.Errorf("Op = %q", reqErr.Op)
	}
}

// TestRequestError_PreservesContextSemantics.
//
// errors.Is against context.Canceled and context.DeadlineExceeded must keep
// working through this package's wrapper AND through the *url.Error the http
// client adds. Callers already have code that depends on those checks; a client
// library that breaks them forces every caller to special-case it.
func TestRequestError_PreservesContextSemantics(t *testing.T) {
	block := make(chan struct{})
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(block) })

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(testContext(t))
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		_, err := client.Users.List(ctx, lightweight.UserListOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) is false for %#v", err)
		}
		var reqErr *lightweight.RequestError
		if !errors.As(err, &reqErr) {
			t.Errorf("error is %T, want *RequestError", err)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(testContext(t), 30*time.Millisecond)
		defer cancel()

		_, err := client.Users.List(ctx, lightweight.UserListOptions{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("errors.Is(err, context.DeadlineExceeded) is false for %#v", err)
		}
	})
}

// TestContext_IsCheckedBeforeAnyRequestIsSent — an already-cancelled context
// must not cost a round trip.
func TestContext_IsCheckedBeforeAnyRequestIsSent(t *testing.T) {
	client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"roles":[],"count":0}`))

	ctx, cancel := context.WithCancel(testContext(t))
	cancel()

	if _, err := client.Roles.List(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n := ts.count(); n != 0 {
		t.Errorf("%d request(s) reached the server despite a cancelled context", n)
	}
}

// TestAPIError_SurfacesRetryAfterWithoutActingOnIt.
//
// The hint is surfaced because a backend needs it to pace itself. It is not
// acted on, because this package cannot know whether the operation it just
// failed is safe to repeat.
func TestAPIError_SurfacesRetryAfterWithoutActingOnIt(t *testing.T) {
	client, ts := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", testRequestID)
		w.Header().Set("Retry-After", "3")
		w.Header().Set("RateLimit-Limit", "20")
		w.Header().Set("RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(errorEnvelope(lightweight.CodeRateLimitExceeded, "Rate limit exceeded for this credential")))
	})

	_, err := client.Users.List(testContext(t), lightweight.UserListOptions{})

	var apiErr *lightweight.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T: %v", err, err)
	}
	if apiErr.Code != lightweight.CodeRateLimitExceeded {
		t.Errorf("Code = %q", apiErr.Code)
	}
	d, ok := apiErr.RetryAfter()
	if !ok {
		t.Fatal("RetryAfter reports no hint, but the response carried one")
	}
	if d != 3*time.Second {
		t.Errorf("RetryAfter = %v, want 3s", d)
	}
	if n := ts.count(); n != 1 {
		t.Errorf("%d requests were sent; a rate-limited call must not be retried automatically", n)
	}
}

// TestAPIError_RetryAfterReportsAbsenceRatherThanZero.
//
// "Retry immediately" and "the server said nothing" call for different
// behaviour. A zero duration that could mean either would have callers hammering
// a server that never asked to be hammered.
func TestAPIError_RetryAfterReportsAbsenceRatherThanZero(t *testing.T) {
	cases := map[string]string{
		"no header":       "",
		"unparseable":     "soon",
		"negative second": "-5",
	}

	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			client, _ := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if header != "" {
					w.Header().Set("Retry-After", header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(errorEnvelope(lightweight.CodeRateLimitExceeded, "slow down")))
			})

			_, err := client.Roles.List(testContext(t))
			var apiErr *lightweight.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error is %T", err)
			}
			if _, ok := apiErr.RetryAfter(); ok {
				t.Error("RetryAfter reports a hint the response did not give")
			}
		})
	}
}

// TestTransport_NeverRetries.
//
// Not on a 500, not on a 503, not on a GET. This package has no idempotency keys
// to make a retried mutation safe and no way to know whether a failed request
// was applied before the answer was lost, so the caller decides.
func TestTransport_NeverRetries(t *testing.T) {
	statuses := []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusTooManyRequests,
	}

	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client, ts := newTestServer(t, jsonResponse(status, errorEnvelope("provider_unavailable", "nope")))

			_, _ = client.Users.List(testContext(t), lightweight.UserListOptions{})
			if n := ts.count(); n != 1 {
				t.Errorf("%d requests for one call on a %d", n, status)
			}

			_, _ = client.Users.Create(testContext(t), lightweight.CreateUserRequest{Email: "a@b.test"})
			if n := ts.count(); n != 2 {
				t.Errorf("a failed mutation was repeated: %d total requests", n)
			}
		})
	}
}

// TestTransport_TolerantOfUnknownResponseFields.
//
// A server that adds an optional field must not break clients compiled before it
// existed. This is why DisallowUnknownFields is not used, and the property is
// worth a test because turning it on looks like a tightening rather than a
// breaking change.
func TestTransport_TolerantOfUnknownResponseFields(t *testing.T) {
	body := `{
		"users":[{
			"id":"9c1e6679-7425-40de-944b-e07fc1f90ae7",
			"username":"ada@example.test",
			"email":"ada@example.test",
			"first_name":"Ada","last_name":"Lovelace",
			"enabled":true,"email_verified":true,
			"created_at":"2026-08-10T14:03:11Z",
			"federation_link":"a-field-from-a-newer-server",
			"totp_enabled":true
		}],
		"first":0,"max":20,"count":1,
		"server_side_pagination_token":"also-new"
	}`
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, body))

	page, err := client.Users.List(testContext(t), lightweight.UserListOptions{})
	if err != nil {
		t.Fatalf("a response with new optional fields was rejected: %v", err)
	}
	if len(page.Users) != 1 || page.Users[0].Email != "ada@example.test" {
		t.Errorf("the known fields did not decode: %+v", page)
	}
}

// TestTransport_NoContentResponsesDecodeCleanly — the deletes and revocations
// answer 204 with no body, which must not be treated as a malformed success.
func TestTransport_NoContentResponsesDecodeCleanly(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", testRequestID)
		w.WriteHeader(http.StatusNoContent)
	})

	const userID = "9c1e6679-7425-40de-944b-e07fc1f90ae7"
	if err := client.Users.Delete(testContext(t), userID); err != nil {
		t.Errorf("Users.Delete: %v", err)
	}
	if err := client.Sessions.RevokeAllForUser(testContext(t), userID); err != nil {
		t.Errorf("Sessions.RevokeAllForUser: %v", err)
	}
	if err := client.Roles.Revoke(testContext(t), userID, "support"); err != nil {
		t.Errorf("Roles.Revoke: %v", err)
	}
}

// TestServices_RejectEmptyPathArgumentsWithoutARoundTrip.
//
// An empty id does not fail cleanly: it builds a URL that addresses a DIFFERENT
// endpoint. `Delete(ctx, "")` would issue DELETE on the collection rather than
// on a missing user, so this is caught locally rather than sent.
func TestServices_RejectEmptyPathArgumentsWithoutARoundTrip(t *testing.T) {
	client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{}`))
	ctx := testContext(t)

	calls := map[string]func() error{
		"Users.Get":                func() error { _, err := client.Users.Get(ctx, ""); return err },
		"Users.Update":             func() error { _, err := client.Users.Update(ctx, "", lightweight.UpdateUserRequest{}); return err },
		"Users.Delete":             func() error { return client.Users.Delete(ctx, "") },
		"Users.SendPasswordReset":  func() error { return client.Users.SendPasswordReset(ctx, "") },
		"Roles.Get":                func() error { _, err := client.Roles.Get(ctx, ""); return err },
		"Roles.Update":             func() error { _, err := client.Roles.Update(ctx, "", lightweight.UpdateRoleRequest{}); return err },
		"Roles.Delete":             func() error { return client.Roles.Delete(ctx, "") },
		"Roles.ListUsers":          func() error { _, err := client.Roles.ListUsers(ctx, ""); return err },
		"Roles.ListForUser":        func() error { _, err := client.Roles.ListForUser(ctx, ""); return err },
		"Roles.Grant":              func() error { return client.Roles.Grant(ctx, "", "support") },
		"Roles.Grant no roles":     func() error { return client.Roles.Grant(ctx, "user-id") },
		"Roles.Revoke":             func() error { return client.Roles.Revoke(ctx, "user-id", "") },
		"Sessions.Revoke":          func() error { return client.Sessions.Revoke(ctx, "") },
		"Sessions.ListForUser":     func() error { _, err := client.Sessions.ListForUser(ctx, ""); return err },
		"Sessions.RevokeAllForUse": func() error { return client.Sessions.RevokeAllForUser(ctx, "") },
		"Invitations.Resend":       func() error { _, err := client.Invitations.Resend(ctx, ""); return err },
		"Invitations.Revoke":       func() error { return client.Invitations.Revoke(ctx, "") },
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			before := ts.count()
			err := call()
			if err == nil {
				t.Fatal("an empty path argument was accepted")
			}
			if !errors.Is(err, lightweight.ErrInvalidArgument) {
				t.Errorf("error does not wrap ErrInvalidArgument: %v", err)
			}
			if ts.count() != before {
				t.Error("a request was sent for an argument that could not produce a meaningful one")
			}
		})
	}
}

// TestErrors_NeverContainTheAPIKey is the security sweep.
//
// A unique sentinel key is used, every failure mode this package can produce is
// provoked, and every resulting error string is searched for it. This is the
// check that would catch a well-meaning change like "include the request headers
// in the error to make debugging easier".
func TestErrors_NeverContainTheAPIKey(t *testing.T) {
	ctx := testContext(t)

	scenarios := map[string]func(t *testing.T) error{
		"api error": func(t *testing.T) error {
			client, _ := newTestServer(t, jsonResponse(http.StatusForbidden,
				errorEnvelope(lightweight.CodeInsufficientScope, "no")))
			_, err := client.Users.List(ctx, lightweight.UserListOptions{})
			return err
		},
		"500 with an unparseable body": func(t *testing.T) error {
			client, _ := newTestServer(t, jsonResponse(http.StatusInternalServerError, "not json at all"))
			_, err := client.Roles.List(ctx)
			return err
		},
		"malformed success": func(t *testing.T) error {
			client, _ := newTestServer(t, jsonResponse(http.StatusOK, `{"users":`))
			_, err := client.Users.List(ctx, lightweight.UserListOptions{})
			return err
		},
		"transport failure": func(t *testing.T) error {
			dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			url := dead.URL
			dead.Close()
			client, cErr := lightweight.NewClient(lightweight.Config{
				BaseURL: url, WorkspaceID: testWorkspace, APIKey: testAPIKey,
			})
			if cErr != nil {
				t.Fatalf("NewClient: %v", cErr)
			}
			_, err := client.Users.List(ctx, lightweight.UserListOptions{})
			return err
		},
		"context timeout": func(t *testing.T) error {
			block := make(chan struct{})
			client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-block:
				case <-r.Context().Done():
				}
			})
			t.Cleanup(func() { close(block) })
			timed, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
			defer cancel()
			_, err := client.Users.List(timed, lightweight.UserListOptions{})
			return err
		},
		"local argument rejection": func(t *testing.T) error {
			client, _ := newTestServer(t, jsonResponse(http.StatusOK, `{}`))
			return client.Users.Delete(ctx, "")
		},
	}

	// The secret segment on its own, so a partial leak is caught as well as a
	// whole-key one.
	secretSegment := testAPIKey[strings.LastIndexByte(testAPIKey, '_')+1:]

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			err := scenario(t)
			if err == nil {
				t.Fatal("the scenario produced no error")
			}

			// Every rendering a caller could plausibly use.
			renderings := []string{
				err.Error(),
				fmt.Sprintf("%v", err),
				fmt.Sprintf("%+v", err),
				fmt.Sprintf("%s", err),
				fmt.Sprintf("%#v", err),
				fmt.Sprintf("%v", fmt.Errorf("wrapping: %w", err)),
			}
			for i, rendered := range renderings {
				if strings.Contains(rendered, testAPIKey) {
					t.Errorf("rendering %d contains the whole API key:\n%s", i, rendered)
				}
				if strings.Contains(rendered, secretSegment) {
					t.Errorf("rendering %d contains the key's secret segment:\n%s", i, rendered)
				}
				if strings.Contains(rendered, "canaryvalue") {
					t.Errorf("rendering %d contains part of the key:\n%s", i, rendered)
				}
			}
		})
	}
}
