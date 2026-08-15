package logging

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ─── Why this file exists ───────────────────────────────────────────────────
//
// Found by the Slice 11 browser suite, on its first green run: the artifact
// scan reported that an OAuth authorization code appears verbatim in the
// application log.
//
// gin.Default() installs gin.Logger(), whose default formatter prints
// param.Path — and param.Path is the request URI INCLUDING the raw query. The
// admin console's PKCE callback is a plain browser navigation to
//
//	GET /admin?code=<authorization code>&state=<csrf state>
//
// so every operator login wrote a live authorization code into the access log.
//
// ─── How bad, honestly ──────────────────────────────────────────────────────
//
// Not critical, and saying otherwise would be inflation. The code is
// single-use, short-lived, and bound to a PKCE verifier that never leaves the
// browser — an attacker holding only the log cannot exchange it. The reasons
// to fix it anyway:
//
//   - the protection is PKCE, not the log. A deployment that ever runs a
//     confidential or non-PKCE client on this surface has a directly usable
//     credential sitting in a log file;
//   - `state` is a CSRF token, and logging it is straightforwardly wrong;
//   - the same formatter prints every OTHER query string too, so the exposure
//     is "whatever any client puts in a query parameter, forever", and the
//     next such parameter will not be found by a test;
//   - logs get shipped, and the moment they are, the blast radius is not the
//     operator's laptop.
//
// So: redact the values of a known-sensitive parameter set, keep the names, and
// keep everything else byte-identical to what gin printed before.
//
// Keeping the NAME visible matters. `/admin?code=REDACTED` still tells an
// operator that this line is a login callback; dropping the query entirely
// would make the login and the console shell load indistinguishable in a log,
// which is a real cost during an incident.

// sensitiveQueryParams — parameter names whose VALUE is credential material.
//
// A denylist, not an allowlist, and that is a deliberate trade. An allowlist
// would be safer and would also redact `?search=`, `?first=`, `?status=` and
// every other parameter an operator reads a log to see — turning the access
// log into a list of paths. The names below are the ones this product and the
// OAuth/OIDC specs it implements actually carry secrets in.
//
// Matching is case-insensitive and covers the whole name, so `access_token`
// and `id_token_hint` are listed explicitly rather than inferred from a
// substring rule that would also catch `token_count`.
var sensitiveQueryParams = map[string]struct{}{
	// OAuth 2.0 / OIDC — RFC 6749 §4.1, RFC 7636, OIDC RP-Initiated Logout.
	"code":          {},
	"state":         {},
	"session_state": {},
	"code_verifier": {},
	"id_token_hint": {},
	"access_token":  {},
	"refresh_token": {},
	"id_token":      {},
	// Not part of any flow this product implements, but they are the
	// conventional names a caller would reach for, and a log is the wrong
	// place to find out we did not think of one.
	"token":         {},
	"secret":        {},
	"client_secret": {},
	"password":      {},
	"api_key":       {},
	"apikey":        {},
}

// redactedPlaceholder is what replaces a sensitive value. Chosen to be
// obviously not-a-value so nobody debugging spends a minute wondering whether
// a client really sent the literal string.
const redactedPlaceholder = "REDACTED"

// RedactRequestURI returns the request URI with the values of
// sensitive query parameters replaced.
//
// Total: any input, including a malformed query, produces a usable string.
// A log formatter that can fail is a log formatter that loses the line it was
// most needed for, so an unparseable query is redacted WHOLESALE rather than
// passed through — the failure mode of "too little detail" is strictly better
// than "the parameter we could not parse was the one carrying the token".
func RedactRequestURI(uri string) string {
	if uri == "" {
		return uri
	}
	path, rawQuery, found := strings.Cut(uri, "?")
	if !found || rawQuery == "" {
		return uri
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return path + "?" + redactedPlaceholder
	}

	changed := false
	for name, vals := range values {
		if _, sensitive := sensitiveQueryParams[strings.ToLower(name)]; !sensitive {
			continue
		}
		for i := range vals {
			// An empty value carries nothing, and blanking it would make
			// "?code=" look like a redaction that happened.
			if vals[i] != "" {
				vals[i] = redactedPlaceholder
				changed = true
			}
		}
	}
	if !changed {
		return uri
	}

	// url.Values.Encode sorts by key, so the reconstructed query may not be in
	// the order the client sent it. Acceptable for a log line, and preferable
	// to hand-rolling a serializer that has to re-derive escaping rules.
	return path + "?" + values.Encode()
}

// AccessLogFormatter is gin's default log formatter with the request URI
// passed through RedactRequestURI.
//
// The layout is reproduced exactly — same fields, same widths, same colour
// handling — because an access log format is an interface: somebody's grep,
// somebody's log shipper's parser. This change is about one field's CONTENT,
// and changing the shape at the same time would make it impossible to tell
// which change broke a downstream consumer.
func AccessLogFormatter(param gin.LogFormatterParams) string {
	var statusColor, methodColor, resetColor string
	if param.IsOutputColor() {
		statusColor = param.StatusCodeColor()
		methodColor = param.MethodColor()
		resetColor = param.ResetColor()
	}

	if param.Latency > time.Minute {
		param.Latency = param.Latency.Truncate(time.Second)
	}

	return fmt.Sprintf("[GIN] %v |%s %3d %s| %13v | %15s |%s %-7s %s %#v\n%s",
		param.TimeStamp.Format("2006/01/02 - 15:04:05"),
		statusColor, param.StatusCode, resetColor,
		param.Latency,
		param.ClientIP,
		methodColor, param.Method, resetColor,
		RedactRequestURI(param.Path),
		param.ErrorMessage,
	)
}

// AccessLogger is the middleware to mount in place of gin.Logger().
//
// gin.Default() is deliberately NOT used by this project's server anymore:
// it hard-wires gin.Logger(), and there is no configuration hook that lets a
// caller transform the path before it is printed.
func AccessLogger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(AccessLogFormatter)
}
