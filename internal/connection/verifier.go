package connection

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Check names. Stable strings — they appear in the verify report a client
// renders, so renaming one is an API change.
const (
	CheckReachable    = "reachable"
	CheckRealmExists  = "realm_exists"
	CheckClientAuth   = "client_authenticated"
	CheckRealmRead    = "realm_readable"
	CheckUsersListing = "users_listable"

	// CheckWriteCapable reports whether the service account was PROVEN to hold
	// a grant permitting identity writes. Added when TD-024 was resolved.
	//
	// Its OK=false carries two distinguishable meanings, separated by Detail:
	// "the provider says this account cannot write" and "the provider did not
	// say". Both are honest; neither is `full`.
	CheckWriteCapable = "write_capable"
)

// Check is one probe result.
type Check struct {
	// Name is one of the Check* constants.
	Name string `json:"name" example:"client_authenticated"`
	// OK is whether this specific probe passed.
	OK bool `json:"ok"`
	// Detail is a short human-readable explanation, safe to show an operator.
	// It never contains the client secret, the access token, or a provider
	// response body — a provider's error page can echo back anything.
	Detail string `json:"detail" example:"authenticated as service account"`
}

// VerifyReport is the structured outcome of a verification run.
type VerifyReport struct {
	// OK is the verdict that drives Health. See Verifier for what it requires.
	OK bool `json:"ok"`
	// AccessMode is what the admin client turned out to be allowed to do.
	AccessMode AccessMode `json:"access_mode" example:"full" enums:"unknown,full,read_only,limited"`
	// Checks are in execution order, and stop at the first failure that makes
	// later ones meaningless (no point listing users if the token failed).
	Checks []Check `json:"checks"`
	// Summary is a one-line verdict for logs and for the connection's
	// health_message.
	Summary string `json:"summary" example:"provider reachable, realm found, client authenticated, full admin access"`
	// CheckedAt is when the run completed.
	CheckedAt time.Time `json:"checked_at"`
}

// VerifyTarget is everything the probe needs. The secret arrives opened, is
// used, and is never stored on the report.
type VerifyTarget struct {
	BaseURL      string
	Realm        string
	ClientID     string
	ClientSecret string
}

// Verifier probes a provider and reports what it found.
//
// The interface exists so the service and the HTTP layer can be tested without
// a live provider — the same reason Repository is an interface. It is a
// consumer-side contract with one production implementation, not a plugin seam.
type Verifier interface {
	Verify(ctx context.Context, target VerifyTarget) VerifyReport
}

// KeycloakVerifier probes a Keycloak realm.
//
// It is a read-only probe, and that is a hard rule: it creates no test user,
// writes nothing, and changes no provider state. An operator must be able to
// press Verify against production without wondering what it left behind.
//
// It does NOT reuse identity/keycloak.AdminClient. That client collapses every
// failure into ErrAdminAPIUnavailable, which is exactly the distinction this
// report exists to make — unreachable, wrong realm, bad credentials and
// insufficient privileges are four different operator actions. The cost is a
// second, simpler client_credentials call; the benefit is a report that says
// which thing is wrong.
type KeycloakVerifier struct {
	client *http.Client
}

// NewKeycloakVerifier builds a verifier. A nil client gets a default with a
// bounded timeout — the probe is operator-driven, but it must not hang a
// request thread on an unresponsive provider.
func NewKeycloakVerifier(client *http.Client) *KeycloakVerifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &KeycloakVerifier{client: client}
}

// verifyTimeout bounds a whole verification run, independent of the HTTP
// client's per-request timeout: five sequential requests each taking the full
// per-request budget would otherwise hold a connection open far too long.
const verifyTimeout = 20 * time.Second

// Verify runs the probe sequence and returns a report. It never returns an
// error: every failure is a check result, because "the provider refused our
// credentials" is an answer, not a malfunction of this API.
//
// OK requires the first three checks — reachable, realm exists, client
// authenticates. The remaining checks determine AccessMode instead. A service
// account that authenticates but cannot list users is correctly configured and
// under-privileged; calling that unhealthy would conflate "you typed the URL
// wrong" with "grant this account realm-management roles", which want very
// different fixes.
//
// # Write capability without writing (TD-024)
//
// The fifth check answers "may this account mutate?" without mutating
// anything. It reads the grants Keycloak itself stamped into the
// client_credentials access token obtained in step 3: a service account's
// realm-management client roles appear under `resource_access`, so the
// provider has already told us what it will allow. No extra request, no
// throwaway role created and deleted, nothing left behind on a production
// realm an operator pressed Verify against.
//
// The trade is that the evidence is only present when the client's scope
// includes those roles — the Keycloak default, but not guaranteed. When it is
// absent the probe reports `unknown` and says so. It never guesses upward.
func (v *KeycloakVerifier) Verify(ctx context.Context, target VerifyTarget) VerifyReport {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	report := VerifyReport{
		AccessMode: AccessModeUnknown,
		Checks:     make([]Check, 0, 6),
	}
	base := strings.TrimRight(target.BaseURL, "/")

	// 1 + 2. The realm's OIDC discovery document answers both "can we reach
	// this host" and "does this realm exist" in one request: a transport error
	// means unreachable, a 404 means the realm is not there.
	discoveryURL := base + "/realms/" + url.PathEscape(target.Realm) + "/.well-known/openid-configuration"
	status, _, err := v.get(ctx, discoveryURL, "")
	switch {
	case err != nil:
		report.Checks = append(report.Checks,
			check(CheckReachable, false, "could not reach the provider: "+transportReason(err)))
		report.Summary = "provider unreachable"
		report.CheckedAt = time.Now().UTC()
		return report

	case status == http.StatusNotFound:
		report.Checks = append(report.Checks,
			check(CheckReachable, true, "provider responded"),
			check(CheckRealmExists, false, "realm not found at this provider"))
		report.Summary = "realm not found"
		report.CheckedAt = time.Now().UTC()
		return report

	case status != http.StatusOK:
		report.Checks = append(report.Checks,
			check(CheckReachable, true, "provider responded"),
			check(CheckRealmExists, false, "realm discovery returned HTTP "+itoa(status)))
		report.Summary = "realm discovery failed"
		report.CheckedAt = time.Now().UTC()
		return report
	}
	report.Checks = append(report.Checks,
		check(CheckReachable, true, "provider responded"),
		check(CheckRealmExists, true, "realm discovery document found"))

	// 3. Service-account authentication.
	token, authDetail, ok := v.authenticate(ctx, base, target)
	report.Checks = append(report.Checks, check(CheckClientAuth, ok, authDetail))
	if !ok {
		report.Summary = "admin client authentication failed"
		report.CheckedAt = time.Now().UTC()
		return report
	}

	// Past this point the connection is usable. What remains grades it.
	report.OK = true

	adminBase := base + "/admin/realms/" + url.PathEscape(target.Realm)

	// 4. Read the realm.
	realmOK, realmDetail := v.probeRead(ctx, adminBase, token, "realm settings")
	report.Checks = append(report.Checks, check(CheckRealmRead, realmOK, realmDetail))

	// 5. List users — capped at one row. This is a read; it creates nothing.
	usersOK, usersDetail := v.probeRead(ctx, adminBase+"/users?max=1", token, "user listing")
	report.Checks = append(report.Checks, check(CheckUsersListing, usersOK, usersDetail))

	// A connection whose READS were refused is graded before the write
	// question is even asked: `limited` already means "under-privileged in a
	// way that makes writes very unlikely", and appending a write verdict to
	// it would suggest a precision this state does not have.
	if !realmOK || !usersOK {
		report.AccessMode = AccessModeLimited
		report.Summary = "provider reachable and client authenticated, but the service account lacks realm-management privileges"
		report.CheckedAt = time.Now().UTC()
		return report
	}

	// 6. Write capability, proven from the grants inside our own access token.
	writeOK, writeProven, writeDetail := inspectWriteGrant(token)
	report.Checks = append(report.Checks, check(CheckWriteCapable, writeOK, writeDetail))

	switch {
	case writeOK:
		report.AccessMode = AccessModeFull
		report.Summary = "provider reachable, realm found, client authenticated, full admin access"
	case writeProven:
		report.AccessMode = AccessModeReadOnly
		report.Summary = "provider reachable and readable, but the service account has no write privileges"
	default:
		report.AccessMode = AccessModeUnknown
		report.Summary = "provider reachable and readable; write capability could not be determined"
	}

	report.CheckedAt = time.Now().UTC()
	return report
}

// writeGrantRoles are the realm-management roles that prove this service
// account may perform identity writes.
//
// `realm-admin` is Keycloak's composite covering everything. `manage-users` is
// the specific grant behind the writes this product performs most — create,
// update, delete, disable, credential and session mutations all route through
// it. `manage-realm` is deliberately NOT here: it permits realm-role writes
// while leaving every user mutation refused, so treating it as write capability
// would reproduce exactly the over-claim TD-024 is about, one endpoint over.
var writeGrantRoles = []string{"realm-admin", "manage-users"}

// realmManagementClient is the Keycloak client whose roles gate admin writes.
const realmManagementClient = "realm-management"

// inspectWriteGrant reads the granted roles out of a service-account access
// token and decides what they prove.
//
// Returns:
//
//	capable — a write grant is present
//	proven  — the token carried usable evidence either way, so a false
//	          `capable` means "provably cannot write" rather than "unknown"
//	detail  — operator-facing prose, safe to render
//
// The token's signature is NOT verified, and that is correct here rather than
// merely tolerable: this is not authentication. The token was minted by the
// provider we just authenticated to, over the operator's configured transport,
// and handed to us in the same function seconds earlier. We are reading OUR OWN
// credential's grant sheet, not deciding whether to trust a caller. The worst a
// forged value could do is make this API claim LESS capability than it has,
// because an unparseable or unexpected token degrades to `unknown`.
func inspectWriteGrant(token string) (capable, proven bool, detail string) {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return false, false, "could not read the granted roles from the provider's token; write capability is unproven"
	}

	entry, present := claims.ResourceAccess[realmManagementClient]
	if !present {
		// The client's scope does not carry realm-management roles. Common
		// when "full scope allowed" is turned off; says nothing about what the
		// account may actually do.
		return false, false, "the provider's token does not list realm-management roles (client scope), so write capability is unproven"
	}

	granted := make(map[string]bool, len(entry.Roles))
	for _, r := range entry.Roles {
		granted[r] = true
	}
	for _, want := range writeGrantRoles {
		if granted[want] {
			return true, true, "service account holds realm-management/" + want + ", so writes are permitted"
		}
	}
	return false, true, "service account holds realm-management roles but none that permit writes (grant it manage-users and re-verify)"
}

// jwtClaims is the sliver of a Keycloak access token this probe reads.
type jwtClaims struct {
	ResourceAccess map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

// decodeJWTClaims base64url-decodes a JWT's payload segment. It returns ok=false
// for anything that is not a three-segment token with a JSON payload — an
// opaque token, a truncated string, a provider that returns something else
// entirely. Every one of those degrades the report to `unknown`.
func decodeJWTClaims(token string) (jwtClaims, bool) {
	var out jwtClaims

	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return out, false
	}
	// Bound the payload before decoding: this string arrived over the network
	// and there is no reason to allocate megabytes for a claim set.
	if len(parts[1]) > 64*1024 {
		return out, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return out, false
	}
	return out, true
}

// authenticate performs the client_credentials grant, returning the token and a
// detail string safe to show an operator.
func (v *KeycloakVerifier) authenticate(ctx context.Context, base string, target VerifyTarget) (token, detail string, ok bool) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", target.ClientID)
	form.Set("client_secret", target.ClientSecret)

	tokenURL := base + "/realms/" + url.PathEscape(target.Realm) + "/protocol/openid-connect/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "could not build the token request", false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return "", "token endpoint unreachable: " + transportReason(err), false
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		// Keycloak returns 401 for both an unknown client and a wrong secret,
		// and it is right not to distinguish them. Neither do we.
		return "", "client id or client secret was rejected", false
	case http.StatusBadRequest:
		// Most often "client is not enabled for service accounts", which is
		// the single most common misconfiguration here — worth naming, because
		// the operator's next click is in a different Keycloak screen.
		return "", "provider rejected the grant; check that the client is confidential and has service accounts enabled", false
	default:
		return "", "token endpoint returned HTTP " + itoa(resp.StatusCode), false
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.AccessToken == "" {
		return "", "token endpoint returned no access token", false
	}
	return payload.AccessToken, "authenticated as service account", true
}

// probeRead issues an authenticated GET and classifies the outcome.
func (v *KeycloakVerifier) probeRead(ctx context.Context, endpoint, token, what string) (bool, string) {
	status, _, err := v.get(ctx, endpoint, token)
	switch {
	case err != nil:
		return false, "could not read " + what + ": " + transportReason(err)
	case status == http.StatusOK:
		return true, "read " + what
	case status == http.StatusForbidden:
		return false, "service account is not permitted to read " + what + " (grant it realm-management roles)"
	case status == http.StatusUnauthorized:
		return false, "token was rejected when reading " + what
	default:
		return false, "reading " + what + " returned HTTP " + itoa(status)
	}
}

// get issues a GET, optionally bearer-authenticated, and returns the status and
// a bounded body.
func (v *KeycloakVerifier) get(ctx context.Context, endpoint, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, body, nil
}

// transportReason renders a transport failure without leaking the full URL.
//
// A raw net/http error embeds the request URL, which for a connection probe
// contains the operator's internal hostnames — fine in a log, not in an API
// response that a lower-privileged UI might render. The classification below is
// what an operator can actually act on anyway.
func transportReason(err error) string {
	if err == nil {
		return "unknown error"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "Timeout"):
		return "timed out"
	case strings.Contains(msg, "no such host"):
		return "host does not resolve"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "certificate"), strings.Contains(msg, "tls:"), strings.Contains(msg, "x509"):
		return "TLS handshake failed"
	case strings.Contains(msg, "unsupported protocol scheme"):
		return "base_url is not a valid http or https URL"
	default:
		return "network error"
	}
}

func check(name string, ok bool, detail string) Check {
	return Check{Name: name, OK: ok, Detail: detail}
}

// itoa avoids pulling strconv in for three call sites of small positive ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
