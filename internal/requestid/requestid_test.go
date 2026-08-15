package requestid

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newProbeRouter mounts the middleware and an endpoint that echoes whatever
// FromContext sees, so a test can assert on the handler's view and the
// response header independently.
func newProbeRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Middleware())
	r.GET("/probe", func(c *gin.Context) {
		c.String(http.StatusOK, FromContext(c))
	})
	return r
}

func get(r *gin.Engine, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		req.Header.Set(Header, header)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestMiddleware_GeneratesWhenAbsent covers the common case: no upstream
// correlation id, so the API mints one and echoes it.
func TestMiddleware_GeneratesWhenAbsent(t *testing.T) {
	r := newProbeRouter()
	w := get(r, "")

	id := w.Body.String()
	if id == "" {
		t.Fatal("FromContext returned empty inside the request")
	}
	if got := w.Header().Get(Header); got != id {
		t.Errorf("response header = %q, handler saw %q; they must match", got, id)
	}
	if !valid(id) {
		t.Errorf("generated id %q would not pass its own validation", id)
	}
}

// TestMiddleware_GeneratesUniquePerRequest — an id shared across requests
// correlates nothing.
func TestMiddleware_GeneratesUniquePerRequest(t *testing.T) {
	r := newProbeRouter()

	seen := make(map[string]bool, 16)
	for i := 0; i < 16; i++ {
		id := get(r, "").Body.String()
		if seen[id] {
			t.Fatalf("request id %q reused across requests", id)
		}
		seen[id] = true
	}
}

// TestMiddleware_HonoursValidInboundHeader is the point of accepting the
// header at all: a correlation id assigned by a gateway or a calling service
// must survive into this API's logs.
func TestMiddleware_HonoursValidInboundHeader(t *testing.T) {
	r := newProbeRouter()

	for _, in := range []string{
		"3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"01hq3v8m2k9x7c4d5e6f7g8h9j", // ULID-ish
		"trace-id_123.4:5",
		"a",
		strings.Repeat("a", maxInboundLength),
	} {
		w := get(r, in)
		if got := w.Body.String(); got != in {
			t.Errorf("inbound %q was replaced with %q", in, got)
		}
		if got := w.Header().Get(Header); got != in {
			t.Errorf("inbound %q echoed as %q", in, got)
		}
	}
}

// TestMiddleware_ReplacesHostileInboundHeader is the security case. The value
// lands in a log line and a response header, so CR/LF (header injection, log
// forging), oversized values and non-ASCII must never be reflected. A bad
// correlation id is replaced rather than rejected — it is not a reason to fail
// an otherwise valid request.
func TestMiddleware_ReplacesHostileInboundHeader(t *testing.T) {
	r := newProbeRouter()

	hostile := map[string]string{
		"crlf injection":  "abc\r\nX-Admin: true",
		"newline":         "abc\ndef",
		"carriage return": "abc\rdef",
		"too long":        strings.Repeat("a", maxInboundLength+1),
		"space":           "abc def",
		"non-ascii":       "abcé",
		"null byte":       "abc\x00def",
		"html":            "<script>alert(1)</script>",
		"slash":           "abc/def",
		"semicolon":       "abc;def",
	}

	for name, in := range hostile {
		t.Run(name, func(t *testing.T) {
			w := get(r, in)

			got := w.Body.String()
			if got == in {
				t.Fatalf("hostile inbound %q was reflected verbatim", in)
			}
			if !valid(got) {
				t.Errorf("replacement id %q is itself invalid", got)
			}
			// The response must carry no trace of the hostile value.
			for _, header := range w.Header() {
				for _, v := range header {
					if strings.Contains(v, "X-Admin") || strings.ContainsAny(v, "\r\n") {
						t.Errorf("hostile content reached a response header: %q", v)
					}
				}
			}
		})
	}
}

// TestFromContext_EmptyWithoutMiddleware pins the deliberate choice not to
// generate an id on read. An id that appears in an error body but in no log
// line and no response header is worse than none, because it looks
// correlatable and is not.
func TestFromContext_EmptyWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/probe", func(c *gin.Context) {
		c.String(http.StatusOK, FromContext(c))
	})

	if got := get(r, "").Body.String(); got != "" {
		t.Errorf("FromContext without the middleware = %q, want empty", got)
	}
}

// TestFromContext_IgnoresNonStringValue guards the type assertion against
// another middleware claiming the same context key.
func TestFromContext_IgnoresNonStringValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/probe", func(c *gin.Context) {
		c.Set(contextKey, 42)
		c.String(http.StatusOK, FromContext(c))
	})

	if got := get(r, "").Body.String(); got != "" {
		t.Errorf("FromContext with a non-string value = %q, want empty", got)
	}
}

// TestMiddleware_MatchesHeaderCaseInsensitively — Go normalizes header case,
// so a client sending x-request-id or X-REQUEST-ID must be honoured too.
func TestMiddleware_MatchesHeaderCaseInsensitively(t *testing.T) {
	r := newProbeRouter()

	for _, spelling := range []string{"x-request-id", "X-REQUEST-ID", "X-Request-Id"} {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set(spelling, "upstream-id-1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if got := w.Body.String(); got != "upstream-id-1" {
			t.Errorf("header spelled %q was not honoured: got %q", spelling, got)
		}
	}
}
