// Package auth — principals.
//
// Before Slice 7 this API had exactly one kind of caller: a human operator
// holding an OIDC token, represented by *Identity. A second kind now exists — a
// backend holding a project credential — and the two must never be mistaken for
// one another.
//
// The representation is a discriminated union rather than a hierarchy. There
// are two cases, they share almost nothing, and an interface with two
// implementations would let a handler forget which one it had. A struct with a
// Type field and two nilable pointers cannot be read without deciding which
// case is in hand.
//
// # What deliberately did NOT change
//
// Identity, StoreIdentity, IdentityFrom, RequireAuth, RequireRole and
// RequireLiveAdmin are untouched, and /admin/* still uses exactly those. A
// project credential NEVER produces an Identity, so every piece of code that
// asks IdentityFrom — the self-protection guards in identity.Service, the
// admin-role gates, the legacy audit actor — sees "no identity" for a project
// rather than a plausible-looking impostor. Failing to find an identity is
// already handled everywhere as 401 or as "unknown actor", so a project reaching
// operator-shaped code fails closed by construction rather than by review.
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

// ProjectTokenPrefix is the fixed prefix every project credential carries.
//
// It lives here rather than in internal/project because it is what the
// authentication middleware discriminates on, and the discrimination has to be
// deterministic: a token starting with this prefix is NEVER handed to the JWT
// parser, and a token without it is NEVER handed to the project authenticator.
//
// The two spaces cannot overlap. A compact JWT is base64url of a JSON header,
// so it always begins "eyJ"; nothing that begins "lw_sk_" can be one. That is
// what makes this a prefix test rather than a try-then-fall-back heuristic,
// which would leak timing and would parse attacker-controlled input twice.
const ProjectTokenPrefix = "lw_sk_"

// PrincipalType discriminates the union.
type PrincipalType string

const (
	// PrincipalOperator is a human using the console, authenticated by OIDC
	// against the installation's realm.
	PrincipalOperator PrincipalType = "operator"

	// PrincipalProject is a backend authenticated by a project credential.
	PrincipalProject PrincipalType = "project"
)

// Principal is the authenticated caller.
//
// Exactly one of Operator and Project is non-nil, matching Type. Construct one
// through NewOperatorPrincipal or NewProjectPrincipal so that invariant cannot
// be broken by a struct literal missing a field.
type Principal struct {
	Type     PrincipalType
	Operator *Identity
	Project  *ProjectPrincipal
}

// ProjectPrincipal is a machine caller: which project, which credential, which
// workspace it is bound to, and what it may do.
//
// WorkspaceID is the authorization boundary and is resolved when the credential
// is authenticated, from the project row. It is carried on the principal rather
// than looked up later so the binding check is a string comparison performed
// before any workspace, connection, secret or provider is touched.
type ProjectPrincipal struct {
	// ProjectID is the public `prj_` id.
	ProjectID string
	// ProjectName is carried for audit readability only. It is never a key.
	ProjectName string
	// CredentialID is the public `key_` id of the credential presented.
	CredentialID string
	// WorkspaceID is the public `ws_` id this project is permanently bound to.
	WorkspaceID string
	// Scopes are the credential's effective scopes, as stored.
	Scopes []string
}

// NewOperatorPrincipal wraps a validated OIDC identity.
func NewOperatorPrincipal(id *Identity) *Principal {
	return &Principal{Type: PrincipalOperator, Operator: id}
}

// NewProjectPrincipal wraps an authenticated project credential.
func NewProjectPrincipal(p *ProjectPrincipal) *Principal {
	return &Principal{Type: PrincipalProject, Project: p}
}

// IsOperator reports whether this principal is a human operator.
func (p *Principal) IsOperator() bool {
	return p != nil && p.Type == PrincipalOperator && p.Operator != nil
}

// IsProject reports whether this principal is a machine caller.
func (p *Principal) IsProject() bool {
	return p != nil && p.Type == PrincipalProject && p.Project != nil
}

// principalGinKey is where the middleware stores the resolved principal. A
// different key from identityGinKey, so nothing can read one as the other.
const principalGinKey = "auth.principal"

// StorePrincipal is called by AuthenticatePrincipal after a caller is resolved.
func StorePrincipal(c *gin.Context, p *Principal) {
	c.Set(principalGinKey, p)
}

// PrincipalFrom returns the authenticated principal for this request.
func PrincipalFrom(c *gin.Context) (*Principal, bool) {
	v, ok := c.Get(principalGinKey)
	if !ok {
		return nil, false
	}
	p, ok := v.(*Principal)
	return p, ok
}

// ProjectAuthenticator resolves an opaque project credential.
//
// The interface lives here and its implementation lives in internal/project,
// the same seam shape AdminChecker uses: the middleware depends on a one-method
// contract, and the domain package that owns the storage implements it. Without
// this, auth would have to import project and project would have to import auth.
//
// Contract, and both halves matter:
//
//   - (nil, nil) means the credential is not usable, FOR ANY REASON — unknown
//     prefix, wrong secret, revoked, expired, or the project is archived. The
//     caller cannot distinguish them, which is exactly the point: a public
//     response that separated them would be a credential-enumeration oracle.
//   - a non-nil error means the authenticator could not decide (the database
//     was unreachable). That is a 503, not a 401: telling a correctly
//     configured backend that its credential is invalid during an outage would
//     send an operator to rotate keys that were never the problem.
type ProjectAuthenticator interface {
	AuthenticateCredential(ctx context.Context, token string) (*ProjectPrincipal, error)
}

// PrincipalConfig is what AuthenticatePrincipal needs.
type PrincipalConfig struct {
	// Provider validates operator OIDC tokens. Required.
	Provider AuthProvider

	// Projects authenticates project credentials. nil is legal and means this
	// installation has no project surface: a `lw_sk_` token is then refused
	// exactly as an unknown credential would be, with no hint that the feature
	// exists elsewhere.
	Projects ProjectAuthenticator
}

// AuthenticatePrincipal resolves WHO the caller is, and nothing else.
//
// It performs no authorization: no role check, no live-admin check, no scope
// check, no workspace binding. Those belong to internal/authz and run after it.
// Merging them would produce a middleware that cannot be reasoned about — the
// operator path would carry authorization the project path must not, and the
// project path would carry authorization the operator path must not.
//
// Discrimination is by the fixed ProjectTokenPrefix, never by attempting one
// parser and falling back to the other.
//
// Failure responses use the /v1 envelope, which is what the rest of this
// surface promises (docs/WORKSPACE_IDENTITY_API.md §3) and what the previous
// reuse of RequireAuth did not deliver.
func AuthenticatePrincipal(cfg PrincipalConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, kind := extractBearer(c)
		if raw == "" {
			EmitEvent(AuthEvent{
				Kind:   kind,
				Reason: "missing or malformed Authorization header",
				Path:   c.Request.URL.Path,
				Method: c.Request.Method,
			})
			abortUnauthorized(c)
			return
		}

		if strings.HasPrefix(raw, ProjectTokenPrefix) {
			authenticateProject(c, cfg.Projects, raw)
			return
		}
		authenticateOperator(c, cfg.Provider, raw)
	}
}

func authenticateOperator(c *gin.Context, p AuthProvider, raw string) {
	if p == nil {
		abortUnauthorized(c)
		return
	}
	id, err := p.ValidateToken(c.Request.Context(), raw)
	if err != nil {
		EmitEvent(AuthEvent{
			Kind:   EventValidationFailed,
			Reason: err.Error(),
			Path:   c.Request.URL.Path,
			Method: c.Request.Method,
		})
		abortUnauthorized(c)
		return
	}

	// Both are stored. Identity keeps every existing consumer working
	// unchanged; Principal is what the authorization layer reads.
	StoreIdentity(c, id)
	StorePrincipal(c, NewOperatorPrincipal(id))
	EmitEvent(AuthEvent{
		Kind:    EventTokenValidated,
		Subject: id.Subject,
		Path:    c.Request.URL.Path,
		Method:  c.Request.Method,
	})
	c.Next()
}

func authenticateProject(c *gin.Context, pa ProjectAuthenticator, raw string) {
	if pa == nil {
		// No project surface on this installation. Answering exactly as for an
		// unknown credential means a probe cannot detect whether the feature
		// exists but is unconfigured.
		abortUnauthorized(c)
		return
	}

	principal, err := pa.AuthenticateCredential(c.Request.Context(), raw)
	if err != nil {
		EmitEvent(AuthEvent{
			Kind:   EventValidationFailed,
			Reason: "project credential lookup failed: " + err.Error(),
			Path:   c.Request.URL.Path,
			Method: c.Request.Method,
		})
		// 503, not 401. See ProjectAuthenticator's contract.
		writeAuthError(c, http.StatusServiceUnavailable, "authorization_unavailable",
			"Credential verification is temporarily unavailable")
		return
	}
	if principal == nil {
		EmitEvent(AuthEvent{
			Kind:   EventValidationFailed,
			Reason: "project credential rejected",
			Path:   c.Request.URL.Path,
			Method: c.Request.Method,
		})
		abortUnauthorized(c)
		return
	}

	// Deliberately NO StoreIdentity here. A project has no Keycloak subject,
	// and manufacturing one would make every operator-shaped check downstream
	// silently applicable to a machine.
	StorePrincipal(c, NewProjectPrincipal(principal))
	EmitEvent(AuthEvent{
		Kind:    EventTokenValidated,
		Subject: principal.ProjectID,
		Path:    c.Request.URL.Path,
		Method:  c.Request.Method,
	})
	c.Next()
}

// abortUnauthorized writes the single public authentication failure.
//
// One code for every cause. An operator token that expired, a project
// credential that was revoked, one that never existed and one whose project was
// archived are indistinguishable from outside — the real reason went to
// EmitEvent, which is the security-observability channel, and not to the audit
// ring, which a scanner could otherwise flood until real history rolled out.
func abortUnauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", `Bearer error="invalid_token"`)
	writeAuthError(c, http.StatusUnauthorized, "credential_invalid",
		"The provided credential is not valid")
}

// authErrorResponse mirrors the /v1 envelope used by internal/workspace,
// internal/connection and internal/identityruntime. It is redeclared rather
// than imported for the same reason those three redeclare it: this package sits
// below all of them, and importing any one of them here would invert the
// dependency direction.
type authErrorResponse struct {
	Error authErrorBody `json:"error"`
}

type authErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func writeAuthError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, authErrorResponse{Error: authErrorBody{
		Code:      code,
		Message:   message,
		RequestID: requestid.FromContext(c),
	}})
}
