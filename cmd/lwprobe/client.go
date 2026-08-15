package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client is a LIGHTWEIGHT consumer.
//
// These three fields are the whole configuration surface, and the type is
// deliberately closed: there is no field for a Keycloak base URL, a realm, a
// client id, a client secret or a connection id, so a change that made the
// consumer need one could not be written without changing this struct — which
// is the architectural claim the slice is making, expressed as a type.
//
//	LIGHTWEIGHT_URL           where the API is
//	LIGHTWEIGHT_WORKSPACE_ID  which tenant to act on
//	LIGHTWEIGHT_API_KEY       who is asking
//
// The workspace id is arguably redundant — the credential is already bound to
// exactly one workspace server-side, so the API could infer it. It is required
// in the path anyway because a URL that means something different depending on
// the key presented is a URL nobody can read, log or cache, and because the
// mismatch is the check that proves the binding is enforced rather than assumed.
type Client struct {
	BaseURL     string
	WorkspaceID string
	APIKey      string

	http *http.Client
}

// NewClient builds a consumer. The timeout is a consumer's choice, not part of
// the contract.
func NewClient(baseURL, workspaceID, apiKey string) *Client {
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		WorkspaceID: workspaceID,
		APIKey:      apiKey,
		http:        &http.Client{Timeout: 30 * time.Second},
	}
}

// Response is what a consumer can see: status, the error envelope when there is
// one, the headers it is documented to react to, and the raw body.
type Response struct {
	Status  int
	Code    string // error.code, empty on success
	Message string
	// Field names the request field a validation failure is about. Absent for
	// every error that is not about a field.
	Field     string
	RequestID string // error.request_id
	Header    http.Header
	Body      []byte
}

// HeaderRequestID is the correlation id echoed on every /v1 response.
func (r *Response) HeaderRequestID() string { return r.Header.Get("X-Request-Id") }

// Do issues a request against the workspace-scoped surface.
//
// path is relative to /v1/workspaces/<workspace>, so a caller writes "/users"
// and cannot accidentally address another tenant by string-building a URL.
func (c *Client) Do(method, path string, body any) (*Response, error) {
	return c.doAbsolute(method, "/v1/workspaces/"+c.WorkspaceID+path, body)
}

// DoForWorkspace addresses a DIFFERENT workspace with this client's key. It
// exists only so the harness can prove the binding is enforced; a real consumer
// has no reason to call it.
func (c *Client) DoForWorkspace(workspaceID, method, path string, body any) (*Response, error) {
	return c.doAbsolute(method, "/v1/workspaces/"+workspaceID+path, body)
}

// DoRaw addresses an arbitrary /v1 path — used for the control-plane routes a
// credential must NOT reach.
func (c *Client) DoRaw(method, path string, body any) (*Response, error) {
	return c.doAbsolute(method, path, body)
}

func (c *Client) doAbsolute(method, path string, body any) (*Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}

	// Every /v1 error is documented as one envelope. Decoding it here rather
	// than per call site is what a thin SDK would do, and whether that is
	// possible at all is one of the things this harness is measuring.
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Field     string `json:"field"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &envelope) == nil {
		out.Code = envelope.Error.Code
		out.Message = envelope.Error.Message
		out.Field = envelope.Error.Field
		out.RequestID = envelope.Error.RequestID
	}
	return out, nil
}

// ─── The operations a backend actually performs ─────────────────────────────

func (c *Client) ListUsers() (*Response, error) { return c.Do(http.MethodGet, "/users", nil) }

// CreateUser provisions a user.
//
// temporary_password is REQUIRED — a backend cannot create a user without
// choosing a credential for them. The only alternative, an invitation, needs
// working SMTP on the workspace's realm.
//
// Slice 8 found this constraint invisible from outside: the field was not
// marked required in the OpenAPI document, and omitting it answered a bare
// `invalid_request`. Both were fixed in Slice 9 — the document marks it, and
// the error names it in `error.field`. CreateUserWithoutPassword below is what
// keeps that true.
func (c *Client) CreateUser(email, first, last string) (*Response, error) {
	return c.Do(http.MethodPost, "/users", map[string]any{
		"email": email, "first_name": first, "last_name": last,
		"temporary_password": "probe-temporary-9task",
	})
}

// CreateUserWithoutPassword is the same call with the field omitted. It exists
// only to measure the quality of the resulting error, which is what an SDK's
// users will actually hit.
func (c *Client) CreateUserWithoutPassword(email string) (*Response, error) {
	return c.Do(http.MethodPost, "/users", map[string]any{
		"email": email, "first_name": "No", "last_name": "Password",
	})
}

func (c *Client) GetUser(id string) (*Response, error) {
	return c.Do(http.MethodGet, "/users/"+id, nil)
}

func (c *Client) DeleteUser(id string) (*Response, error) {
	return c.Do(http.MethodDelete, "/users/"+id, nil)
}

func (c *Client) ListRoles() (*Response, error) { return c.Do(http.MethodGet, "/roles", nil) }

func (c *Client) CreateRole(name string) (*Response, error) {
	return c.Do(http.MethodPost, "/roles", map[string]any{"name": name})
}

func (c *Client) AssignRoles(userID string, roles []string) (*Response, error) {
	return c.Do(http.MethodPost, "/users/"+userID+"/roles", map[string]any{"roles": roles})
}

func (c *Client) ListUserRoles(userID string) (*Response, error) {
	return c.Do(http.MethodGet, "/users/"+userID+"/roles", nil)
}

func (c *Client) ListUserSessions(userID string) (*Response, error) {
	return c.Do(http.MethodGet, "/users/"+userID+"/sessions", nil)
}

func (c *Client) ListSessions() (*Response, error) { return c.Do(http.MethodGet, "/sessions", nil) }

func (c *Client) SetPassword(userID, password string) (*Response, error) {
	return c.Do(http.MethodPut, "/users/"+userID+"/password",
		map[string]any{"password": password, "temporary": false})
}

// userIDFrom pulls the id out of a create/list payload. The shape is part of
// the contract, so failing to find it is a contract failure, not a helper bug.
func userIDFrom(body []byte) (string, error) {
	var single struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &single); err == nil && single.ID != "" {
		return single.ID, nil
	}
	return "", fmt.Errorf("no id in response: %s", truncate(string(body), 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ─── Configuration ──────────────────────────────────────────────────────────

// Config separates the three values a consumer needs from the extra fixtures
// the HARNESS needs to provoke each documented failure. The split is the point:
// the first group is the product's promise, the second exists only because a
// test has to be handed a revoked key rather than revoke one itself.
type Config struct {
	// The consumer contract.
	URL         string
	WorkspaceID string
	APIKey      string

	// Harness fixtures — every one is another credential or id, never a piece
	// of provider configuration.
	KeyB              string // second live credential, same project
	KeyReadOnly       string // users:read only
	KeyRevoked        string // revoked before the run
	KeyExpired        string // expired before the run
	KeyArchived       string // belongs to an archived project
	KeyDeadProvider   string // bound to a workspace whose realm is gone
	ForeignWorkspace  string // a workspace this key is not bound to
	DeadProviderWS    string // the workspace KeyDeadProvider is bound to
	RevocableKey      string // live at start; the harness reports it for the operator to revoke
	BenchConcurrency  int
	BenchRequests     int
	SkipRateLimitTest bool

	// ─── Negative-matrix fixtures (Slice 14 / KI-018) ───────────────────────
	//
	// Every one of these is a credential, a workspace id or an opaque resource
	// id. None is a provider URL, a realm name or a client secret, so the
	// closed-configuration claim the Client type makes still holds: this
	// program can attack the boundary from outside without ever being told
	// where the boundary's other side lives.

	// NegativePhase selects warm or matrix. See runNegative.
	NegativePhase string

	KeyAuditOnly     string // audit:read and nothing else
	KeyNoAudit       string // every identity scope, but NOT audit:read
	KeySecondProject string // a second project in the SAME workspace, disjoint scopes

	// The workspace that gets archived between the two phases, and a
	// credential bound to it.
	KeyArchivableWS string
	ArchivableWS    string

	// The workspace whose only connection gets retired between the two phases,
	// and a credential bound to it.
	KeyLosesConnection string
	LosesConnectionWS  string

	// A workspace whose Keycloak service account can read but was never
	// granted the roles a write needs. The provider-forbidden fixture.
	KeyProviderReadOnly string
	ProviderReadOnlyWS  string

	// OwnUserID lives in this credential's realm; ForeignUserID lives in
	// another workspace's realm. Both are valid Keycloak user ids, which is
	// what makes the cross-realm case an attack rather than a typo.
	OwnUserID     string
	ForeignUserID string

	// PasswordSentinel is a unique string used as a temporary password in
	// rejected calls, so an artifact scan can prove it reached neither the
	// realm, a response, nor a log.
	PasswordSentinel string
}

func loadConfig() (*Config, error) {
	c := &Config{
		URL:              os.Getenv("LIGHTWEIGHT_URL"),
		WorkspaceID:      os.Getenv("LIGHTWEIGHT_WORKSPACE_ID"),
		APIKey:           os.Getenv("LIGHTWEIGHT_API_KEY"),
		KeyB:             os.Getenv("LW_KEY_B"),
		KeyReadOnly:      os.Getenv("LW_KEY_READONLY"),
		KeyRevoked:       os.Getenv("LW_KEY_REVOKED"),
		KeyExpired:       os.Getenv("LW_KEY_EXPIRED"),
		KeyArchived:      os.Getenv("LW_KEY_ARCHIVED"),
		KeyDeadProvider:  os.Getenv("LW_KEY_DEAD_PROVIDER"),
		ForeignWorkspace: os.Getenv("LW_FOREIGN_WORKSPACE_ID"),
		DeadProviderWS:   os.Getenv("LW_DEAD_PROVIDER_WORKSPACE_ID"),
		RevocableKey:     os.Getenv("LW_KEY_REVOCABLE"),
		BenchConcurrency: envInt("LW_BENCH_CONCURRENCY", 8),
		BenchRequests:    envInt("LW_BENCH_REQUESTS", 400),
	}
	c.SkipRateLimitTest = os.Getenv("LW_SKIP_RATE_LIMIT_TEST") == "true"

	c.NegativePhase = envOrDefault("LW_NEG_PHASE", "matrix")
	c.KeyAuditOnly = os.Getenv("LW_KEY_AUDIT_ONLY")
	c.KeyNoAudit = os.Getenv("LW_KEY_NO_AUDIT")
	c.KeySecondProject = os.Getenv("LW_KEY_SECOND_PROJECT")
	c.KeyArchivableWS = os.Getenv("LW_KEY_ARCHIVABLE_WS")
	c.ArchivableWS = os.Getenv("LW_ARCHIVABLE_WORKSPACE_ID")
	c.KeyLosesConnection = os.Getenv("LW_KEY_LOSES_CONNECTION")
	c.LosesConnectionWS = os.Getenv("LW_LOSES_CONNECTION_WORKSPACE_ID")
	c.KeyProviderReadOnly = os.Getenv("LW_KEY_PROVIDER_READONLY")
	c.ProviderReadOnlyWS = os.Getenv("LW_PROVIDER_READONLY_WORKSPACE_ID")
	c.OwnUserID = os.Getenv("LW_OWN_USER_ID")
	c.ForeignUserID = os.Getenv("LW_FOREIGN_USER_ID")
	c.PasswordSentinel = envOrDefault("LW_PASSWORD_SENTINEL", "lw-neg-sentinel-pw-do-not-log")

	var missing []string
	if c.URL == "" {
		missing = append(missing, "LIGHTWEIGHT_URL")
	}
	if c.WorkspaceID == "" {
		missing = append(missing, "LIGHTWEIGHT_WORKSPACE_ID")
	}
	if c.APIKey == "" {
		missing = append(missing, "LIGHTWEIGHT_API_KEY")
	}
	if len(missing) > 0 {
		return nil, errors.New("missing " + strings.Join(missing, ", "))
	}
	return c, nil
}

// envOrDefault reads an environment variable, substituting a default for an
// unset or empty value. Distinct from bench.go's envOr, which is used before
// the config is loaded.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return fallback
	}
	return n
}

// Client builds the consumer described by the three contract values.
func (c *Config) Client() *Client { return NewClient(c.URL, c.WorkspaceID, c.APIKey) }

// ClientWith builds a consumer with a different key, same workspace.
func (c *Config) ClientWith(key string) *Client { return NewClient(c.URL, c.WorkspaceID, key) }
