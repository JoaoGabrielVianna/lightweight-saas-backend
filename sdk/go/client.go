package lightweight

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// APIKeyPrefix is the fixed prefix every Project Credential carries.
//
// Exported because it is genuinely useful outside this package: it is what a
// secret scanner matches on, and what a caller validating its own configuration
// at startup can check without importing a regexp.
const APIKeyPrefix = "lw_sk_"

// workspaceIDPrefix is the public-id prefix for a workspace.
const workspaceIDPrefix = "ws_"

// DefaultTimeout is the timeout applied to the http.Client this package builds
// when the caller does not supply one.
//
// It exists because a client with NO timeout and a caller who forgets a context
// deadline hangs forever, and "forever" is the one failure mode a backend cannot
// recover from. It is deliberately generous: the point is to bound a wedged
// connection, not to be the mechanism by which anyone expresses a deadline.
//
// It applies ONLY to the default client. A caller who sets [Config.HTTPClient]
// owns the timeout entirely — this package does not add to, shorten or otherwise
// second-guess it. Per-request deadlines belong on the context, which is checked
// first and reported as [RequestError] wrapping context.DeadlineExceeded.
const DefaultTimeout = 30 * time.Second

// Config is everything a Client needs.
//
// The first three fields are the contract a backend is promised, and are the
// only ones with no default. The absence of a field for a provider URL, a
// tenant name, a client id or a client secret is deliberate and is pinned by a
// test: a change that made this package need one would be a change to the
// product's central claim, not an implementation detail.
type Config struct {
	// BaseURL is where LIGHTWEIGHT is served, e.g. https://identity.example.com.
	// A trailing slash and a path prefix are both accepted; the scheme must be
	// http or https.
	BaseURL string

	// WorkspaceID is the workspace this client acts on, in the form
	// ws_<uuid>. The bare UUID is also accepted and normalised.
	//
	// It must be the workspace the API key is bound to. It is not a security
	// control — the server enforces the binding regardless of what is sent — but
	// getting it wrong is a configuration error worth failing on, and every
	// request this client builds addresses this workspace and no other.
	WorkspaceID string

	// APIKey is the Project Credential, in the form lw_sk_<lookup>_<secret>.
	//
	// It is sent as a bearer token and never appears anywhere else: not in a
	// URL, not in a query parameter, not in the User-Agent, and not in any error
	// this package produces. [Config] redacts it in its own String and GoString,
	// so printing a Config with %v, %+v or %#v cannot leak it.
	APIKey string

	// HTTPClient, when set, is used verbatim and is authoritative.
	//
	// This package never mutates it, never replaces its Transport, and never
	// touches http.DefaultClient or http.DefaultTransport. Supplying one is how
	// tracing, metrics, proxies, connection tuning and retry policy are added:
	// wrap an http.RoundTripper and this package will not know or care.
	//
	// nil means a private client with [DefaultTimeout] is built for this
	// Client's exclusive use.
	HTTPClient *http.Client

	// UserAgent replaces the default `lightweight-go/<version>`.
	//
	// Set it to identify your service to an operator reading access logs. It is
	// sent verbatim, so include an SDK identifier yourself if you want one to
	// survive.
	UserAgent string
}

// String renders the configuration with the API key redacted.
//
// This is a security control, not a convenience. Without it, the default
// formatting of a struct containing a credential prints the credential — and
// the places a config gets printed (a startup log line, a panic, a %+v in an
// error path someone added at 2am) are exactly the places a secret must not
// reach. Implementing it means there is no formatting verb that renders the key.
func (c Config) String() string {
	return fmt.Sprintf("lightweight.Config{BaseURL:%q, WorkspaceID:%q, APIKey:%s, HTTPClient:%s, UserAgent:%q}",
		c.BaseURL, c.WorkspaceID, redactKey(c.APIKey), presence(c.HTTPClient != nil), c.UserAgent)
}

// GoString covers %#v, which ignores String and would otherwise print the key.
func (c Config) GoString() string { return c.String() }

// presence renders a boolean as prose, so the rendering of a Config cannot be
// mistaken for something that could be parsed back into one.
func presence(set bool) string {
	if set {
		return "<supplied>"
	}
	return "<default>"
}

// redactKey renders an API key safely for a human.
//
// It keeps the fixed prefix and the first four characters of the LOOKUP
// segment, which is stored in clear server-side and is not secret. That is
// enough to tell two keys apart in a log line and nowhere near enough to
// authenticate with. The secret segment is never included, at any length.
func redactKey(key string) string {
	if key == "" {
		return "<empty>"
	}
	if !strings.HasPrefix(key, APIKeyPrefix) {
		// Not one of ours. Say so without echoing whatever it actually is —
		// a value in this field is a credential whatever shape it has.
		return "<redacted>"
	}
	lookup := key[len(APIKeyPrefix):]
	if i := strings.IndexByte(lookup, '_'); i >= 0 {
		lookup = lookup[:i]
	}
	if len(lookup) > 4 {
		lookup = lookup[:4]
	}
	return APIKeyPrefix + lookup + "…"
}

// quote renders a string for the redacting String methods. strconv.Quote rather
// than %q by hand so control characters in caller data cannot rewrite the line
// a redacted value appears on.
func quote(s string) string { return strconv.Quote(s) }

// joinQuoted renders a string slice for the same purpose.
func joinQuoted(values []string) string {
	if values == nil {
		return "nil"
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, strconv.Quote(v))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// Client is a configured LIGHTWEIGHT consumer, bound to one workspace.
//
// It is immutable after construction and safe for concurrent use by any number
// of goroutines. There is no mutable secret state: rotating to a new credential
// means constructing a new Client, which is both simpler to reason about and
// impossible to get half-applied under concurrency.
//
// Construct it with [NewClient] or [NewClientFromEnv]. The zero value is not
// usable.
type Client struct {
	baseURL   *url.URL
	workspace string
	apiKey    string
	userAgent string
	http      *http.Client

	// Users administers user records in this workspace.
	Users *UsersService
	// Roles administers roles and their assignment to users.
	Roles *RolesService
	// Sessions reads and revokes active sessions.
	Sessions *SessionsService
	// Invitations administers pending invitations.
	Invitations *InvitationsService
	// Audit reads the workspace's durable audit trail.
	Audit *AuditService
}

// String renders the client without its credential, for the same reason
// [Config.String] exists.
func (c *Client) String() string {
	if c == nil {
		return "<nil *lightweight.Client>"
	}
	return fmt.Sprintf("lightweight.Client{BaseURL:%q, WorkspaceID:%q, APIKey:%s}",
		c.baseURL, c.workspace, redactKey(c.apiKey))
}

// GoString covers %#v.
func (c *Client) GoString() string { return c.String() }

// WorkspaceID returns the workspace this client is bound to, normalised to
// ws_<uuid>. Useful for log lines and for asserting configuration at startup.
func (c *Client) WorkspaceID() string { return c.workspace }

// BaseURL returns the normalised base URL this client addresses.
func (c *Client) BaseURL() string { return c.baseURL.String() }

// ErrConfig is the sentinel every construction failure wraps.
//
// It exists so a caller can tell "this process is misconfigured, and no amount
// of retrying will help" from every other error this package returns, with one
// errors.Is rather than a type switch.
var ErrConfig = errors.New("lightweight: invalid configuration")

// NewClient validates the configuration and returns a ready client.
//
// Validation is EAGER and total: a base URL that is empty, unparseable, missing
// a host or carrying a scheme other than http/https; a workspace id that is not
// a public id; an API key that is not a Project Credential — each is reported
// here, at startup, rather than as a confusing 401 or a DNS failure on the first
// call in production. Every error wraps [ErrConfig].
//
// No network traffic is generated. Whether the credential is still valid is a
// question only the server can answer, and asking it here would make
// construction fail during an unrelated outage.
func NewClient(cfg Config) (*Client, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	workspace, err := parseWorkspaceID(cfg.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if err := validateAPIKey(cfg.APIKey); err != nil {
		return nil, err
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		// A PRIVATE client, not http.DefaultClient. Sharing the default would
		// mean this package's timeout silently became the timeout of every
		// other library in the process that also reached for it.
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}

	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent()
	}

	c := &Client{
		baseURL:   base,
		workspace: workspace,
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		http:      httpClient,
	}
	c.Users = &UsersService{client: c}
	c.Roles = &RolesService{client: c}
	c.Sessions = &SessionsService{client: c}
	c.Invitations = &InvitationsService{client: c}
	c.Audit = &AuditService{client: c}
	return c, nil
}

// Environment variable names read by [NewClientFromEnv]. Exported so a caller
// can name them in its own startup diagnostics without duplicating the strings.
const (
	EnvBaseURL     = "LIGHTWEIGHT_URL"
	EnvWorkspaceID = "LIGHTWEIGHT_WORKSPACE_ID"
	EnvAPIKey      = "LIGHTWEIGHT_API_KEY"
)

// NewClientFromEnv constructs a client from [EnvBaseURL], [EnvWorkspaceID] and
// [EnvAPIKey].
//
// It exists because those three variables ARE the integration contract, and a
// helper that reads exactly them — and has nowhere to read a fourth from — makes
// that contract executable rather than documented.
func NewClientFromEnv() (*Client, error) {
	return NewClient(Config{
		BaseURL:     os.Getenv(EnvBaseURL),
		WorkspaceID: os.Getenv(EnvWorkspaceID),
		APIKey:      os.Getenv(EnvAPIKey),
	})
}

// parseBaseURL normalises the base URL and rejects what cannot work.
//
// The trailing slash is stripped so path joining is unambiguous, and a path
// prefix is preserved so an installation served under a reverse-proxy subpath
// is supported without a second configuration field.
func parseBaseURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: BaseURL is empty (set %s)", ErrConfig, EnvBaseURL)
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		// err carries the caller's own URL, which is not secret, but it is
		// still their input — quote it rather than interpolate it raw.
		return nil, fmt.Errorf("%w: BaseURL %q is not a URL: %v", ErrConfig, trimmed, err)
	}
	switch u.Scheme {
	case "http", "https":
	case "":
		return nil, fmt.Errorf("%w: BaseURL %q has no scheme (did you mean https://%s?)",
			ErrConfig, trimmed, trimmed)
	default:
		return nil, fmt.Errorf("%w: BaseURL %q has scheme %q; want http or https",
			ErrConfig, trimmed, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: BaseURL %q has no host", ErrConfig, trimmed)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: BaseURL %q carries a query or fragment; it must be an origin, optionally with a path prefix",
			ErrConfig, trimmed)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// parseWorkspaceID accepts ws_<uuid> or a bare <uuid> and returns the ws_ form.
//
// Both spellings are accepted because the server accepts both, and a client
// stricter than the server it talks to is a client that rejects working
// configuration. Normalising to one form here means every URL this package
// builds is spelled the same way, which matters for anything reading access
// logs.
func parseWorkspaceID(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: WorkspaceID is empty (set %s)", ErrConfig, EnvWorkspaceID)
	}
	body := strings.TrimPrefix(trimmed, workspaceIDPrefix)
	if !isUUID(body) {
		return "", fmt.Errorf("%w: WorkspaceID %q is not a workspace id; want ws_<uuid>", ErrConfig, trimmed)
	}
	return workspaceIDPrefix + strings.ToLower(body), nil
}

// validateAPIKey checks what can be checked locally.
//
// The PREFIX is checked and the internal structure is not, and the asymmetry is
// deliberate. The prefix is a published, stable part of the contract — it is
// what secret scanners match and what the server discriminates on — so a value
// without it is certainly not a Project Credential, most often an operator
// bearer token pasted into the wrong variable. The lengths of the segments are
// internal to the server's token format: pinning them here would mean a future
// format change made this client reject keys the server considers valid, which
// is a worse failure than a 401 that says exactly what is wrong.
func validateAPIKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("%w: APIKey is empty (set %s)", ErrConfig, EnvAPIKey)
	}
	if key != strings.TrimSpace(key) {
		return fmt.Errorf("%w: APIKey has leading or trailing whitespace", ErrConfig)
	}
	if !strings.HasPrefix(key, APIKeyPrefix) {
		// The key itself is NEVER interpolated, not even partially, and not
		// even though this branch means it is probably not a real credential:
		// "probably" is not a basis on which to write a secret into an error
		// that will be logged.
		return fmt.Errorf("%w: APIKey is not a Project Credential (it must start with %q); "+
			"an operator bearer token will not work here", ErrConfig, APIKeyPrefix)
	}
	if len(key) <= len(APIKeyPrefix) {
		return fmt.Errorf("%w: APIKey is only the %q prefix", ErrConfig, APIKeyPrefix)
	}
	return nil
}

// isUUID reports whether s is a canonical 8-4-4-4-12 hex UUID. Written out
// rather than pulled in, because one regexp is not worth a dependency in a
// package whose selling point is having none.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'f':
			case c >= 'A' && c <= 'F':
			default:
				return false
			}
		}
	}
	return true
}
