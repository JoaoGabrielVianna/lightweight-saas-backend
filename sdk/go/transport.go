package lightweight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// requestIDHeader is the correlation id LIGHTWEIGHT echoes on every /v1
// response, success or failure.
const requestIDHeader = "X-Request-Id"

// maxErrorBodyBytes bounds how much of an error response is read.
//
// The threat is mundane and real: a reverse proxy having a bad day answers 500
// with a megabytes-long HTML page, and a client that reads it all to build a
// one-line error allocates that page per failed request — at exactly the moment
// it is failing fastest. 64 KiB is orders of magnitude more than the envelope
// this API produces and small enough that a flood of them costs nothing.
const maxErrorBodyBytes = 64 << 10

// maxResponseBodyBytes bounds a success body.
//
// Generous, because a page of users with attributes is legitimately large, and
// bounded, because "the server is trusted" is not a memory-safety argument when
// the thing answering may not be the server. Exceeding it produces a
// [ProtocolError] rather than a truncated decode: a body this client could not
// read in full is a body it must not pretend to have understood.
const maxResponseBodyBytes = 8 << 20

// maxDrainBytes is how much of an unread body is drained to let the connection
// be reused. Beyond this the connection is closed instead, which is cheaper than
// reading a hostile body to the end for the sake of a keep-alive.
const maxDrainBytes = 64 << 10

// do performs one request and decodes the result.
//
// out may be nil for the 204 responses this API returns from deletes and
// revocations; the body is drained and discarded in that case.
//
// # Error discipline
//
// Exactly one of three types comes back, and never anything else:
//
//	*RequestError   no answer was obtained (transport, context)
//	*APIError       an answer that refused
//	*ProtocolError  an answer that could not be read
//
// Collapsing them would be the single most damaging simplification available
// here: a caller cannot distinguish "the user does not exist" from "the network
// is down" if both arrive as errors.New(string).
func (c *Client) do(ctx context.Context, op, method, path string, query url.Values, body, out any) error {
	endpoint := c.baseURL.String() + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			// Marshalling this package's own request types cannot realistically
			// fail, but returning something typed beats a panic if it ever does.
			return &RequestError{Op: op, Method: method, Path: path,
				Err: fmt.Errorf("encoding request body: %w", err)}
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return &RequestError{Op: op, Method: method, Path: path, Err: err}
	}

	// The credential goes in a header and nowhere else. Never a query parameter,
	// where it would be recorded by every proxy, access log and browser history
	// between here and the server.
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// http.Client wraps context errors in *url.Error, which itself contains
		// the full URL. That is fine — the URL holds no secret — and unwrapping
		// preserves errors.Is(err, context.Canceled) through both layers.
		return &RequestError{Op: op, Method: method, Path: path, Err: unwrapURLError(err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.apiError(op, method, path, resp)
	}

	if out == nil {
		return nil
	}

	limited := io.LimitReader(resp.Body, maxResponseBodyBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		// The status said success but the body did not arrive. This is NOT an
		// APIError: the operation may well have been performed server-side, and
		// reporting it as a refusal would tell a caller the opposite of the
		// truth about a mutation.
		return &ProtocolError{Op: op, Method: method, Path: path,
			StatusCode: resp.StatusCode, RequestID: resp.Header.Get(requestIDHeader),
			Err: fmt.Errorf("reading response body: %w", unwrapURLError(err))}
	}
	if len(raw) > maxResponseBodyBytes {
		return &ProtocolError{Op: op, Method: method, Path: path,
			StatusCode: resp.StatusCode, RequestID: resp.Header.Get(requestIDHeader),
			Err: fmt.Errorf("response body exceeds %d bytes", maxResponseBodyBytes)}
	}

	// DisallowUnknownFields is deliberately NOT set. A server that grows an
	// optional field must not break clients compiled before it existed, and
	// forward compatibility is worth more here than catching a typo in a struct
	// tag — which the contract fixtures catch instead.
	if err := json.Unmarshal(raw, out); err != nil {
		return &ProtocolError{Op: op, Method: method, Path: path,
			StatusCode: resp.StatusCode, RequestID: resp.Header.Get(requestIDHeader),
			Err: fmt.Errorf("decoding response: %w", err)}
	}
	return nil
}

// errorEnvelope is the /v1 error contract.
//
// Redeclared here rather than imported, and that duplication is the whole
// architecture: the HTTP contract is the boundary, so this package must be
// able to compile, and to be extracted into its own repository, without the
// server's types in scope.
type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Field     string `json:"field"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

// apiError builds the typed refusal from a non-2xx response.
//
// A body that does not parse does NOT become a ProtocolError. A 503 from a load
// balancer that never reached LIGHTWEIGHT is still a refusal a caller must react
// to, and the status is the actionable part; the empty Code says truthfully that
// no machine-readable reason was given.
func (c *Client) apiError(op, method, path string, resp *http.Response) *APIError {
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Op:         op,
		Method:     method,
		Path:       path,
		RequestID:  resp.Header.Get(requestIDHeader),
	}
	if d, ok := parseRetryAfter(resp.Header); ok {
		apiErr.retryAfter, apiErr.retryAfterSet = d, true
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil || len(raw) == 0 {
		apiErr.Message = http.StatusText(resp.StatusCode)
		return apiErr
	}

	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Error.Code == "" {
		// Not our envelope. Report the status honestly and do NOT echo the body:
		// an unknown intermediary's page is unbounded, may be HTML, and has no
		// business being pasted into a caller's logs by us.
		apiErr.Message = http.StatusText(resp.StatusCode)
		return apiErr
	}

	apiErr.Code = env.Error.Code
	apiErr.Message = env.Error.Message
	apiErr.Field = env.Error.Field
	if env.Error.RequestID != "" {
		// The envelope's copy wins over the header's. They agree in practice;
		// preferring the body means a proxy that strips or rewrites the header
		// cannot cost a caller the correlation id.
		apiErr.RequestID = env.Error.RequestID
	}
	return apiErr
}

// unwrapURLError strips the *url.Error wrapper http.Client adds.
//
// The wrapper's message repeats the method and URL that a [RequestError] already
// carries, so keeping it produces errors that say everything twice. Unwrapping
// preserves errors.Is against context.Canceled and context.DeadlineExceeded,
// which is the property that actually matters.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

// workspacePath builds a path under this client's workspace.
//
// Every request this package makes goes through here, so there is exactly one
// place a workspace id is written into a URL, and it is a field fixed at
// construction. A method cannot address another tenant by string-building,
// because no method builds a URL.
func (c *Client) workspacePath(segments ...string) string {
	var b strings.Builder
	b.WriteString("/v1/workspaces/")
	b.WriteString(c.workspace)
	for _, s := range segments {
		b.WriteByte('/')
		// Escaped, always. A role name is caller-supplied and may legitimately
		// contain characters that would otherwise change which route is
		// addressed; an unescaped "../" would be path traversal against the
		// workspace boundary.
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}
