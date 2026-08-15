package identityruntime

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

// Handler is the workspace-scoped identity HTTP surface.
//
// It owns no provider and no credentials. Every request resolves its own
// provider from the workspace in the path, which is the whole point: two
// concurrent requests for two workspaces obtain two providers and share no
// state, and there is no process-level "current realm" for them to disagree
// about.
//
// Business logic is NOT duplicated here. The resolved provider is wrapped in
// the same identity.Service that backs /admin/*, so every rule that surface
// enforces — page-size clamping, id validation, reserved and protected role
// names, self-delete and last-admin guards, invitation compensation, Keycloak
// error translation — is the one already in use and already tested. This
// package contributes routing, the workspace boundary, an error vocabulary and
// audit attribution, and nothing else.
//
// Authentication and authorization are not handled here either. Every route is
// mounted inside the /v1 group, which carries rate limit → RequireAuth →
// RequireRole("admin") → RequireLiveAdmin. A handler that also checked would be
// a second, divergeable copy of that rule.
type Handler struct {
	resolver *Resolver
}

// NewHandler constructs a Handler. Returns nil when the resolver is nil, so the
// composition root's nil check omits the routes rather than mounting handlers
// that would panic.
func NewHandler(resolver *Resolver) *Handler {
	if resolver == nil {
		return nil
	}
	return &Handler{resolver: resolver}
}

// ---------------------------------------------------------------------------
// The two seams every handler goes through
// ---------------------------------------------------------------------------

// scope is a resolved request: the workspace, its provider wrapped in the
// shared identity service, and the metadata needed to audit what happens next.
type scope struct {
	resolved *Resolved
	service  *identity.Service
}

// workspaceID is the public `ws_` id, for audit attribution.
func (s scope) workspaceID() string { return s.resolved.WorkspacePublicID }

// read resolves the workspace for a read-only operation.
//
// Returns ok=false having ALREADY written the error response, so call sites
// read as `sc, ok := h.read(c); if !ok { return }`. Returning an error for the
// caller to hand back to respondError would work too, and would give every one
// of the twenty-odd handlers a chance to forget.
func (h *Handler) read(c *gin.Context) (scope, bool) {
	resolved, err := h.resolver.ForWorkspace(c.Request.Context(), c.Param("workspace_id"))
	if err != nil {
		respondError(c, err)
		return scope{}, false
	}
	// identity.NewService is a struct literal around one interface value, so
	// this costs nothing per request and buys the guarantee that the
	// workspace-scoped surface cannot drift from the admin one.
	return scope{resolved: resolved, service: identity.NewService(resolved.Provider)}, true
}

// write resolves the workspace for a MUTATING operation, and refuses when the
// active connection is known to be under-privileged.
//
// This is the central enforcement Phase 8 asks for: every mutation in this
// package goes through this one function, and a new mutation that forgets it
// has to have called read() instead — which
// TestHandler_EveryMutationGoesThroughTheWriteGuard detects by walking the
// route table rather than by trusting review.
//
// The guard is a pre-flight, not a capability model. See Resolved.CanWrite for
// what access_mode does and does not tell us, and why a genuinely read-only
// service account is caught by Keycloak's 403 instead.
func (h *Handler) write(c *gin.Context) (scope, bool) {
	sc, ok := h.read(c)
	if !ok {
		return scope{}, false
	}
	if !sc.resolved.CanWrite() {
		respondError(c, ErrConnectionReadOnly)
		return scope{}, false
	}
	return sc, true
}

// callerSubject is the authenticated caller's Keycloak sub, threaded into the
// service so its self-protection guards (no self-delete, no self-disable, no
// self-strip-admin) can fire.
//
// Note what this means across a workspace boundary: the caller authenticated
// against the INSTALLATION's realm, while the target lives in the workspace's
// realm. Two different realms, so the subjects can only collide if the same
// Keycloak backs both — which is exactly the single-realm installation where
// self-protection matters. Where they differ, the guards simply never match,
// and the last-admin guard (which is realm-local) still protects the target
// realm. Documented in docs/WORKSPACE_IDENTITY_API.md §Self-protection.
func callerSubject(c *gin.Context) string {
	if id, ok := auth.IdentityFrom(c); ok && id != nil {
		return id.Subject
	}
	return ""
}

// bind decodes a JSON body, writing invalid_request and returning false on
// failure. Uses encoding/json rather than gin's binding so that decoding and
// validation stay separate concerns: everything this can fail on is a
// malformed body, and field-level rules belong to the identity service.
//
// A type mismatch names the field. `{"enabled": "yes"}` is the single most
// common integration mistake — a client sending a string where a bool belongs —
// and the decoder already knows which field it was. Passing that through costs
// nothing and turns an unactionable 400 into a one-line fix.
//
// The name comes from json.UnmarshalTypeError.Field, which is OUR struct tag,
// not the client's key: an unknown key does not produce a type error at all.
func bind(c *gin.Context, dst any) bool {
	err := json.NewDecoder(c.Request.Body).Decode(dst)
	if err == nil {
		return true
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		// Nested fields arrive as "parent.child"; the leaf is what a client
		// needs, and the dotted form would not match any key they sent.
		field := typeErr.Field
		if i := strings.LastIndexByte(field, '.'); i >= 0 {
			field = field[i+1:]
		}
		respondError(c, ErrInvalidRequest.WithField(field))
		return false
	}

	respondError(c, ErrInvalidRequest)
	return false
}

// pathUserID validates the user id in the path before anything else runs.
//
// Validating here rather than letting the service reject it is what produces
// `invalid_user_id` instead of a generic `invalid_request`. It is not a second
// copy of the rule — identity.IsValidUserID is the service's own predicate.
func pathUserID(c *gin.Context) (string, bool) {
	id := c.Param("user_id")
	if !identity.IsValidUserID(id) {
		respondError(c, ErrInvalidUserID)
		return "", false
	}
	return id, true
}

func pathRoleName(c *gin.Context) (string, bool) {
	name := c.Param("role_name")
	if !identity.IsValidRoleName(name) {
		respondError(c, ErrInvalidRoleName)
		return "", false
	}
	return name, true
}

func pathSessionID(c *gin.Context) (string, bool) {
	id := c.Param("session_id")
	if !identity.IsValidUserID(id) { // session ids are UUIDs too
		respondError(c, ErrInvalidSessionID)
		return "", false
	}
	return id, true
}

func pathInvitationID(c *gin.Context) (string, bool) {
	id := c.Param("invitation_id")
	if !identity.IsValidUserID(id) { // an invitation IS a user
		respondError(c, ErrInvitationNotFound)
		return "", false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// ListUsers handles GET /v1/workspaces/{workspace_id}/users.
//
// @Summary     List a workspace's users
// @Description Returns a page of users from the Keycloak realm that the
// @Description workspace's ACTIVE connection points at. The workspace id in the
// @Description path is the routing boundary: two workspaces connected to two
// @Description realms return two disjoint sets of users.
// @Description
// @Description `first` and `max` in the response are the EFFECTIVE values —
// @Description what the server actually used after clamping `max` to [1, 100]
// @Description and `first` to >= 0. This differs from the legacy
// @Description `GET /admin/users`, which echoes the caller's raw input.
// @Description
// @Description **Required scope (project credentials):** `users:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       search query string false "substring match on username/email/firstName/lastName"
// @Param       first  query int    false "offset (default 0)"
// @Param       max    query int    false "page size (default 20, max 100)"
// @Success     200 {object} identity.ListUsersResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, workspace_connection_missing, workspace_connection_unusable, provider_forbidden"
// @Failure     500 {object} ErrorResponse "provider_credentials_unavailable, internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users [get]
func (h *Handler) ListUsers(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}

	q := identity.ListUsersQuery{Search: c.Query("search")}
	if v := c.Query("first"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			q.First = n
		}
	}
	if v := c.Query("max"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			q.Max = n
		}
	}

	// Clamp BEFORE the call so the response can echo what was really used.
	// The service clamps again and reaches the same answer — the function is
	// idempotent — so this adds a bound, never a second rule. Fixing TD-020
	// here and not on /admin/users is deliberate: see ClampListUsersQuery.
	q = identity.ClampListUsersQuery(q)

	users, err := sc.service.ListUsers(c.Request.Context(), q)
	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}

	out := make([]identity.UserResponse, 0, len(users))
	for _, u := range users {
		out = append(out, identity.NewUserResponse(u))
	}
	c.JSON(http.StatusOK, identity.ListUsersResponse{
		Users: out,
		First: q.First,
		Max:   q.Max,
		Count: len(out),
	})
}

// CreateUser handles POST /v1/workspaces/{workspace_id}/users.
//
// @Summary     Create a user in a workspace
// @Description Provisions a user directly with a temporary password they must
// @Description change on first login. This is the existing direct-provisioning
// @Description flow, not a new one — the alternative is an invitation, which
// @Description sends an email and therefore requires SMTP configured on the
// @Description realm.
// @Description
// @Description If setting the password or assigning roles fails after the user
// @Description was created, the half-provisioned user is removed so the same
// @Description email can be retried.
// @Description
// @Description **Required scope (project credentials):** `users:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       body body CreateUserRequest true "user to create"
// @Success     201 {object} identity.UserResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, workspace_connection_missing, connection_read_only, conflict, provider_forbidden"
// @Failure     500 {object} ErrorResponse "provider_credentials_unavailable, internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users [post]
func (h *Handler) CreateUser(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	var req CreateUserRequest
	if !bind(c, &req) {
		return
	}

	user, err := sc.service.CreateUser(c.Request.Context(), identity.CreateUserRequest{
		Email:             req.Email,
		FirstName:         req.FirstName,
		LastName:          req.LastName,
		TemporaryPassword: req.TemporaryPassword,
		Roles:             req.Roles,
	})

	// The audit target names the email, never the password. Target.Name is
	// the only free-text field on the event and it is set from the request,
	// so this is the one place a credential could plausibly reach a log.
	target := audit.Target{Kind: "user", Name: req.Email}
	if user != nil {
		target.ID = user.ID
	}
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserCreated, target, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.JSON(http.StatusCreated, identity.NewUserResponse(*user))
}

// GetUser handles GET /v1/workspaces/{workspace_id}/users/{user_id}.
//
// @Summary     Get a user in a workspace
// @Description **Required scope (project credentials):** `users:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Success     200 {object} identity.UserResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, workspace_connection_missing, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id} [get]
func (h *Handler) GetUser(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}

	user, err := sc.service.GetUser(c.Request.Context(), userID)
	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.JSON(http.StatusOK, identity.NewUserResponse(*user))
}

// UpdateUser handles PATCH /v1/workspaces/{workspace_id}/users/{user_id}.
//
// @Summary     Update a user in a workspace
// @Description Partial update — omitted fields are preserved. Guarded against
// @Description self-disable and against disabling the realm's last enabled
// @Description admin; both return `caller_forbidden`.
// @Description
// @Description **Required scope (project credentials):** `users:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Param       body body UpdateUserRequest true "fields to change"
// @Success     200 {object} identity.UserResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "caller_forbidden"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id} [patch]
func (h *Handler) UpdateUser(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}
	var req UpdateUserRequest
	if !bind(c, &req) {
		return
	}

	user, err := sc.service.UpdateUser(c.Request.Context(), callerSubject(c), userID, identity.UpdateUserRequest{
		FirstName:     req.FirstName,
		LastName:      req.LastName,
		Email:         req.Email,
		Enabled:       req.Enabled,
		EmailVerified: req.EmailVerified,
	})

	target := audit.Target{Kind: "user", ID: userID}
	if user != nil {
		target.Name = user.Email
	}
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserUpdated, target, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.JSON(http.StatusOK, identity.NewUserResponse(*user))
}

// DeleteUser handles DELETE /v1/workspaces/{workspace_id}/users/{user_id}.
//
// @Summary     Delete a user in a workspace
// @Description Guarded against self-deletion and against removing the realm's
// @Description last enabled admin; both return `caller_forbidden`.
// @Description
// @Description **Required scope (project credentials):** `users:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Success     204
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "caller_forbidden"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id} [delete]
func (h *Handler) DeleteUser(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}

	err := sc.service.DeleteUser(c.Request.Context(), callerSubject(c), userID)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserDeleted,
		audit.Target{Kind: "user", ID: userID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// resourceKind tells the error translator what the request was operating on,
// so identity.ErrNotFound can become `user_not_found` rather than a vague
// `resource_not_found`.
//
// Passed explicitly by each handler because it is the only party that knows.
// Inferring it from the route path would work until someone adds a route whose
// path does not match its subject.
type resourceKind int

const (
	kindUser resourceKind = iota
	kindRole
	kindSession
	kindInvitation
)

func notFoundFor(kind resourceKind) *Error {
	switch kind {
	case kindRole:
		return ErrRoleNotFound
	case kindSession:
		return ErrSessionNotFound
	case kindInvitation:
		return ErrInvitationNotFound
	default:
		return ErrUserNotFound
	}
}

func conflictFor(kind resourceKind) *Error {
	if kind == kindRole {
		return ErrRoleAlreadyExists
	}
	return ErrConflict
}

// translateIdentityError maps the identity package's sentinels onto this
// surface's stable codes.
//
// ORDER IS LOAD-BEARING. Three of these sentinels wrap another one:
//
//	ErrProviderForbidden wraps ErrForbidden
//	ErrRoleProtected     wraps ErrForbidden
//	ErrRoleReserved      wraps ErrBadRequest
//
// so the specific cases must be tested before the general ones. That is what
// makes the wrapping safe for /admin/* — which checks only the general ones and
// is unaffected — while still letting this surface report the difference.
// TestTranslate_SpecificSentinelsWinOverTheirBase pins the ordering.
//
// Errors already carrying a catalogued *Error (workspace resolution failures)
// pass straight through.
func translateIdentityError(err error, kind resourceKind) error {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr
	}

	switch {
	case errors.Is(err, identity.ErrProviderForbidden):
		return ErrProviderForbidden
	case errors.Is(err, identity.ErrRoleProtected):
		return ErrRoleReserved
	case errors.Is(err, identity.ErrRoleReserved):
		return ErrRoleReserved
	case errors.Is(err, identity.ErrNotFound):
		return notFoundFor(kind)
	case errors.Is(err, identity.ErrConflict):
		return conflictFor(kind)
	case errors.Is(err, identity.ErrForbidden):
		// Reached only by the service's own guards, since every provider-side
		// 403 matched ErrProviderForbidden above.
		return ErrCallerForbidden
	case errors.Is(err, identity.ErrBadRequest):
		// Carry the field through when the service named one. This is the
		// whole of TD-029 on the service path: the code and status are
		// unchanged, and what changes is that a client can tell WHICH field to
		// fix instead of re-reading the whole request.
		return ErrInvalidRequest.WithField(identity.FieldOf(err))
	case errors.Is(err, identity.ErrNotConfigured):
		return ErrConnectionUnusable
	case errors.Is(err, identity.ErrAdminAPIUnavailable):
		return ErrProviderUnavailable
	}
	return err
}

// respondError writes the stable /v1 error envelope.
//
// Anything that is not a catalogued *Error is an internal fault: the real error
// is logged with the request id and the client is told only internal_error.
// That is what keeps SQL fragments, constraint names and raw Keycloak response
// bodies off the wire — enforced here, once, rather than at each call site.
func respondError(c *gin.Context, err error) {
	rid := requestid.FromContext(c)

	var domainErr *Error
	if !errors.As(err, &domainErr) {
		log.Error("unhandled workspace-identity error (request_id=" + rid + "): " + err.Error())
		domainErr = ErrInternal
	} else if domainErr.Status >= http.StatusInternalServerError {
		log.Error("workspace-identity " + domainErr.Code + " (request_id=" + rid + "): " + err.Error())
	}

	c.JSON(domainErr.Status, ErrorResponse{Error: ErrorBody{
		Code:      domainErr.Code,
		Message:   domainErr.Message,
		Field:     domainErr.Field,
		RequestID: rid,
	}})
}
