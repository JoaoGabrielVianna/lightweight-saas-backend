package logging

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The defect these tests pin was found by the Slice 11 browser suite: the
// admin console's PKCE callback is a browser navigation to
// `GET /admin?code=…&state=…`, and gin's default access-log formatter printed
// the request URI including that query. Every operator login wrote a live
// authorization code to the log.
//
// The browser suite catches it end to end, which is slow and needs a Keycloak.
// These tests catch it here, which is where the rule lives.

func TestRedactRequestURI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "the PKCE callback — the case this was written for",
			in:   "/admin?code=abc123def&state=xyz789",
			want: "/admin?code=REDACTED&state=REDACTED",
		},
		{
			name: "no query is left exactly alone",
			in:   "/v1/workspaces",
			want: "/v1/workspaces",
		},
		{
			name: "an empty query is left exactly alone",
			in:   "/v1/workspaces?",
			want: "/v1/workspaces?",
		},
		{
			// The whole point of a denylist rather than dropping the query: an
			// operator reading a log needs to see which page was requested.
			// Nothing sensitive here, so the URI is returned byte-for-byte —
			// including the client's original parameter order, which the
			// re-encoding path below does not preserve.
			name: "ordinary parameters survive untouched, in the original order",
			in:   "/v1/workspaces/ws_a/users?search=alice&max=20",
			want: "/v1/workspaces/ws_a/users?search=alice&max=20",
		},
		{
			name: "mixed — only the sensitive value is replaced",
			in:   "/v1/workspaces/ws_a/audit?event=role.created&token=sekrit",
			want: "/v1/workspaces/ws_a/audit?event=role.created&token=REDACTED",
		},
		{
			name: "case-insensitive on the parameter name",
			in:   "/callback?Code=abc&STATE=def",
			want: "/callback?Code=REDACTED&STATE=REDACTED",
		},
		{
			// RP-initiated logout carries the operator's id token.
			name: "logout id_token_hint",
			in:   "/admin?id_token_hint=eyJhbGciOi.payload.sig",
			want: "/admin?id_token_hint=REDACTED",
		},
		{
			// A name that merely CONTAINS a sensitive word is not sensitive.
			// Over-matching would redact the fields an operator reads.
			name: "a similarly named parameter is not redacted",
			in:   "/v1/thing?token_count=5",
			want: "/v1/thing?token_count=5",
		},
		{
			name: "an empty sensitive value is not turned into a fake redaction",
			in:   "/admin?code=",
			want: "/admin?code=",
		},
		{
			// Failing open here would mean the one query we could not parse is
			// the one printed verbatim.
			name: "an unparseable query is redacted wholesale",
			in:   "/admin?%zz=1&code=abc",
			want: "/admin?REDACTED",
		},
		{
			name: "repeated sensitive parameter — every occurrence goes",
			in:   "/admin?code=one&code=two",
			want: "/admin?code=REDACTED&code=REDACTED",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactRequestURI(tc.in); got != tc.want {
				t.Errorf("RedactRequestURI(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAccessLogFormatter_KeepsGinsLayout pins the SHAPE of the line, not just
// the redaction. An access-log format is an interface — somebody's grep,
// somebody's log shipper. This change was allowed to alter one field's
// content; it must not alter the layout, or a downstream break would be
// impossible to attribute.
func TestAccessLogFormatter_KeepsGinsLayout(t *testing.T) {
	params := gin.LogFormatterParams{
		TimeStamp:  time.Date(2026, 8, 10, 17, 4, 5, 0, time.UTC),
		StatusCode: http.StatusOK,
		Latency:    1500 * time.Microsecond,
		ClientIP:   "127.0.0.1",
		Method:     http.MethodGet,
		Path:       "/admin?code=super-secret-code&state=csrf",
	}

	got := AccessLogFormatter(params)

	if !strings.HasPrefix(got, "[GIN] 2026/08/10 - 17:04:05 |") {
		t.Errorf("prefix drifted from gin's default layout:\n%q", got)
	}
	for _, want := range []string{" 200 ", "1.5ms", "127.0.0.1", "GET", `"/admin?code=REDACTED&state=REDACTED"`} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted line is missing %q:\n%q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("formatted line does not end in a newline: %q", got)
	}
	if strings.Contains(got, "super-secret-code") || strings.Contains(got, "csrf") {
		t.Errorf("the formatter printed credential material:\n%q", got)
	}
}

// TestAccessLogger_DoesNotLogAuthorizationCodes is the end of the chain: a real
// gin engine, a real request shaped like the console's PKCE callback, and the
// bytes that actually reach the log writer.
//
// The unit test above proves the redaction function; this proves the redaction
// function is the one the server's middleware uses. Those are different
// claims, and the second is the one that regressed when gin.Default() was
// chosen.
func TestAccessLogger_DoesNotLogAuthorizationCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	prev := gin.DefaultWriter
	gin.DefaultWriter = &buf
	t.Cleanup(func() { gin.DefaultWriter = prev })

	r := gin.New()
	r.Use(AccessLogger())
	r.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/admin?code=live-authorization-code&state=csrf-state", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	logged := buf.String()
	if logged == "" {
		t.Fatal("nothing was logged — the access logger is not wired")
	}
	if strings.Contains(logged, "live-authorization-code") {
		t.Errorf("the access log contains an OAuth authorization code:\n%s", logged)
	}
	if strings.Contains(logged, "csrf-state") {
		t.Errorf("the access log contains an OAuth state token:\n%s", logged)
	}
	if !strings.Contains(logged, "/admin?code=REDACTED") {
		t.Errorf("the callback is no longer identifiable in the log, which defeats the point:\n%s", logged)
	}
}
