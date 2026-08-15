// Package requestid attaches a correlation id to each request and makes it
// readable by handlers.
//
// It exists as its own package rather than living in internal/server because
// both sides need it: server mounts the middleware, and domain handlers read
// the id to put it in their error envelope. internal/server already imports
// the domain packages, so the accessor cannot live there without an import
// cycle.
//
// Scope. The middleware is mounted on the /v1 group only. Mounting it globally
// would add an X-Request-Id header to every /admin/* response, changing a
// surface this work is required to leave byte-compatible.
package requestid

import (
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/gin-gonic/gin"
)

// Header is the request and response header carrying the correlation id.
// The canonical spelling is the one Go's textproto normalization produces,
// so a client sending X-REQUEST-ID or x-request-id is matched too.
const Header = "X-Request-Id"

// contextKey is where the resolved id is stored in the gin context.
const contextKey = "request_id"

// maxInboundLength bounds a client-supplied id. Long enough for a UUID, a
// trace id, or a typical gateway token; short enough that it cannot be used to
// bloat log lines or a response header.
const maxInboundLength = 64

// Middleware resolves a request id, exposes it to handlers, and echoes it to
// the client.
//
// A client-supplied X-Request-Id is honoured so a correlation id assigned
// upstream (a gateway, a calling service) survives into this API's logs — that
// is the whole value of the header. It is validated first, because the value
// is untrusted input that ends up in both a log line and a response header:
// unvalidated, it is a header-injection and log-forging vector. Anything that
// fails validation is silently replaced rather than rejected, since a bad
// correlation id is not a reason to fail an otherwise valid request.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(Header)
		if !valid(id) {
			id = generate()
		}
		c.Set(contextKey, id)
		c.Header(Header, id)
		c.Next()
	}
}

// FromContext returns the request id for this request, or "" when the
// middleware is not mounted.
//
// Returning "" rather than generating one on the fly is deliberate: an id that
// appears in an error body but in no log line and no response header is worse
// than no id, because it looks correlatable and is not.
func FromContext(c *gin.Context) string {
	if v, ok := c.Get(contextKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// valid accepts a bounded run of unreserved URL characters. That covers UUIDs,
// W3C trace ids, ULIDs and typical gateway ids, while excluding the CR, LF and
// non-ASCII bytes that make header injection and log forging possible.
func valid(s string) bool {
	if s == "" || len(s) > maxInboundLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// generate produces a fresh id. It reuses the UUID generator rather than
// introducing a second source of randomness; if crypto/rand fails, the request
// proceeds with an empty id rather than failing, because losing correlation is
// not worth losing the request over.
func generate() string {
	id, err := publicid.New()
	if err != nil {
		return ""
	}
	return id
}
