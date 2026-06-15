package identity

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/gin-gonic/gin"
)

var log = logger.New("identity")

// Handler is the HTTP-layer surface for the identity module. It owns no
// state — every call delegates to Service.
//
// adminInvalidator is the GAP-1 invalidation hook: handlers that mutate role
// membership or user enabled-state call it with the target's subject so the
// auth-tier live-admin cache (internal/auth.CachedAdminChecker) refreshes
// on the next request rather than serving stale "is admin" for up to its
// TTL. nil-safe — defaults to NoopAdminInvalidator at construction.
type Handler struct {
	service          *Service
	adminInvalidator auth.AdminInvalidator
}

// NewHandler constructs a Handler. service may be nil; the caller is
// expected to gate route registration on that condition (rather than
// returning 500 from every endpoint).
func NewHandler(service *Service) *Handler {
	return &Handler{service: service, adminInvalidator: auth.NoopAdminInvalidator{}}
}

// SetAdminInvalidator wires the live-admin-cache invalidation hook. Called
// by the server-tier composition root after constructing the cached
// checker. Passing nil reverts to the no-op invalidator so the handler
// stays safe even if the wiring forgets to wire it.
func (h *Handler) SetAdminInvalidator(inv auth.AdminInvalidator) {
	if inv == nil {
		h.adminInvalidator = auth.NoopAdminInvalidator{}
		return
	}
	h.adminInvalidator = inv
}

// ListUsers handles GET /admin/users.
//
// @Summary     List users (admin)
// @Description Returns a page of users from the Keycloak realm. Requires the
// @Description caller's token to carry the realm `admin` role. Pagination
// @Description is offset-based; `max` is clamped to [1, 100] server-side.
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Param       search query string false "substring match on username/email/firstName/lastName"
// @Param       first  query int    false "offset (default 0)"
// @Param       max    query int    false "page size (default 20, max 100)"
// @Success     200 {object} ListUsersResponse
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     502 {object} map[string]string "upstream identity provider unavailable"
// @Failure     503 {object} map[string]string "identity management not configured"
// @Router      /admin/users [get]
func (h *Handler) ListUsers(c *gin.Context) {
	q := ListUsersQuery{Search: c.Query("search")}
	if v := c.Query("first"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.First = n
		}
	}
	if v := c.Query("max"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Max = n
		}
	}

	users, err := h.service.ListUsers(c.Request.Context(), q)
	if err != nil {
		handleError(c, err)
		return
	}

	out := make([]UserResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u))
	}
	c.JSON(http.StatusOK, ListUsersResponse{
		Users: out,
		First: q.First,
		Max:   q.Max,
		Count: len(out),
	})
}

// GetUser handles GET /admin/users/:id.
//
// @Summary     Get user by id (admin)
// @Description Returns a single user by Keycloak `sub` UUID. Requires the
// @Description caller's token to carry the realm `admin` role.
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Keycloak sub UUID"
// @Success     200 {object} UserResponse
// @Failure     400 {object} map[string]string "malformed id (not a UUID)"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} map[string]string "no user with that id"
// @Failure     502 {object} map[string]string "upstream identity provider unavailable"
// @Failure     503 {object} map[string]string "identity management not configured"
// @Router      /admin/users/{id} [get]
func (h *Handler) GetUser(c *gin.Context) {
	user, err := h.service.GetUser(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toUserResponse(*user))
}

// ─────────────── Roles ───────────────

// ListRoles handles GET /admin/roles.
//
// @Summary     List realm roles (admin)
// @Description Returns every realm role in the Keycloak realm. Client roles
// @Description are filtered out. Built-in roles (offline_access,
// @Description uma_authorization, default-roles-*) are flagged via the
// @Description `builtin` field so callers can disable destructive actions.
// @Tags        roles
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} ListRolesResponse
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /admin/roles [get]
func (h *Handler) ListRoles(c *gin.Context) {
	roles, err := h.service.ListRoles(c.Request.Context())
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]RoleResponse, 0, len(roles))
	for _, r := range roles {
		out = append(out, toRoleResponse(r))
	}
	c.JSON(http.StatusOK, ListRolesResponse{Roles: out, Count: len(out)})
}

// GetRole handles GET /admin/roles/:name.
//
// @Summary     Get realm role by name (admin)
// @Tags        roles
// @Produce     json
// @Security    BearerAuth
// @Param       name path string true "realm role name"
// @Success     200 {object} RoleResponse
// @Failure     400 {object} map[string]string "malformed name"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/roles/{name} [get]
func (h *Handler) GetRole(c *gin.Context) {
	r, err := h.service.GetRole(c.Request.Context(), c.Param("name"))
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toRoleResponse(*r))
}

// ListRoleUsers handles GET /admin/roles/:name/users.
//
// @Summary     List users carrying a role (admin)
// @Tags        roles
// @Produce     json
// @Security    BearerAuth
// @Param       name path string true "realm role name"
// @Success     200 {object} ListUsersResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such role"
// @Failure     502 {object} map[string]string
// @Router      /admin/roles/{name}/users [get]
func (h *Handler) ListRoleUsers(c *gin.Context) {
	users, err := h.service.ListUsersByRole(c.Request.Context(), c.Param("name"))
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]UserResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u))
	}
	c.JSON(http.StatusOK, ListUsersResponse{Users: out, First: 0, Max: 0, Count: len(out)})
}

// ListUserRoles handles GET /admin/users/:id/roles.
//
// @Summary     List a user's realm roles (admin)
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Keycloak sub UUID"
// @Success     200 {object} ListRolesResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/users/{id}/roles [get]
func (h *Handler) ListUserRoles(c *gin.Context) {
	roles, err := h.service.ListUserRoles(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]RoleResponse, 0, len(roles))
	for _, r := range roles {
		out = append(out, toRoleResponse(r))
	}
	c.JSON(http.StatusOK, ListRolesResponse{Roles: out, Count: len(out)})
}

// ─────────────── Sessions ───────────────

// ListSessions handles GET /admin/sessions.
//
// @Summary     List active sessions across the realm (admin)
// @Description Aggregates user-sessions across every enabled client in the
// @Description realm. A session that has touched multiple clients yields a
// @Description single entry whose `clients` map carries every client name.
// @Description One slow or failing client does not break the aggregation;
// @Description failures are silently skipped.
// @Tags        sessions
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} ListSessionsResponse
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/sessions [get]
func (h *Handler) ListSessions(c *gin.Context) {
	sessions, err := h.service.ListSessions(c.Request.Context())
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]SessionResponse, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, toSessionResponse(s))
	}
	c.JSON(http.StatusOK, ListSessionsResponse{Sessions: out, Count: len(out)})
}

// ListUserSessions handles GET /admin/users/:id/sessions.
//
// @Summary     List a user's active sessions (admin)
// @Tags        sessions
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Keycloak sub UUID"
// @Success     200 {object} ListSessionsResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/users/{id}/sessions [get]
func (h *Handler) ListUserSessions(c *gin.Context) {
	sessions, err := h.service.ListUserSessions(c.Request.Context(), c.Param("id"))
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]SessionResponse, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, toSessionResponse(s))
	}
	c.JSON(http.StatusOK, ListSessionsResponse{Sessions: out, Count: len(out)})
}

// ─────────────── Invitations ───────────────

// ─── Stage 5.2B — CREATE handlers ────────────────────────────────────────

// CreateRole handles POST /admin/roles.
//
// @Summary     Create realm role (admin)
// @Description Creates a new realm role in Keycloak. The service normalizes
// @Description the name (lowercase + trim) and rejects reserved names
// @Description (admin, user, offline_access, uma_authorization, default-roles-*).
// @Description Duplicates surface as 409.
// @Tags        roles
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body CreateRoleRequestBody true "role definition"
// @Success     201 {object} RoleResponse
// @Failure     400 {object} map[string]string "invalid name (empty, too long, reserved, or bad characters)"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     409 {object} map[string]string "role already exists"
// @Failure     502 {object} map[string]string "upstream identity provider unavailable"
// @Failure     503 {object} map[string]string "identity management not configured"
// @Router      /admin/roles [post]
func (h *Handler) CreateRole(c *gin.Context) {
	var body CreateRoleRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	role, err := h.service.CreateRole(c.Request.Context(), CreateRoleRequest{
		Name:        body.Name,
		Description: body.Description,
	})
	target := audit.Target{Kind: "role", ID: body.Name, Name: body.Name}
	if err == nil && role != nil {
		target.ID = role.Name
		target.Name = role.Name
	}
	logging.RecordMutation(c, audit.ActionRoleCreated, target, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toRoleResponse(*role))
}

// CreateInvitation handles POST /admin/invitations and the alias POST
// /admin/users/invite.
//
// @Summary     Create invitation (admin)
// @Description Provisions a Keycloak user in invited-but-incomplete state,
// @Description assigns initial realm roles, and dispatches the action email
// @Description (VERIFY_EMAIL + UPDATE_PASSWORD). The new user is enabled but
// @Description cannot fully sign in until they complete both actions.
// @Description When the request body omits `invited_by`, the handler
// @Description defaults it to the authenticated admin's email (or sub).
// @Tags        invitations
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body CreateInvitationRequestBody true "invitation"
// @Success     201 {object} InvitationResponse
// @Failure     400 {object} map[string]string "invalid email, empty roles, past expires_at, etc."
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} map[string]string "one of the requested roles does not exist"
// @Failure     409 {object} map[string]string "a user with this email already exists"
// @Failure     502 {object} map[string]string "upstream identity provider unavailable (often: SMTP not configured)"
// @Failure     503 {object} map[string]string "identity management not configured"
// @Router      /admin/invitations [post]
func (h *Handler) CreateInvitation(c *gin.Context) {
	var body CreateInvitationRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}

	// Default invited_by from the authenticated identity. Empty when there
	// is no identity (shouldn't happen — RequireAuth runs first — but the
	// fallback keeps the contract explicit).
	defaultInvitedBy := ""
	if id, ok := auth.IdentityFrom(c); ok {
		if id.Email != "" {
			defaultInvitedBy = id.Email
		} else {
			defaultInvitedBy = id.Subject
		}
	}

	inv, err := h.service.CreateInvitation(c.Request.Context(), CreateInvitationRequest{
		Email:     body.Email,
		FirstName: body.FirstName,
		LastName:  body.LastName,
		Roles:     body.Roles,
		ExpiresAt: body.ExpiresAt,
		InvitedBy: body.InvitedBy,
	}, defaultInvitedBy)
	target := audit.Target{Kind: "invitation", Name: body.Email}
	if err == nil && inv != nil {
		target.ID = inv.ID
	}
	// CreateInvitation is the only path that provisions a Keycloak user in
	// this codebase, so we also emit ActionUserCreated to keep "create user"
	// audit-traceable for downstream consumers — see docs/AUDIT_WIRING.md.
	logging.RecordMutation(c, audit.ActionInvitationCreated, target, err)
	userTarget := audit.Target{Kind: "user", ID: target.ID, Name: body.Email}
	logging.RecordMutation(c, audit.ActionUserCreated, userTarget, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toInvitationResponse(*inv))
}

// ListInvitations handles GET /admin/invitations.
//
// @Summary     List pending invitations (admin)
// @Description Synthesizes an "invitations" view from Keycloak users whose
// @Description state implies an invitation: required actions pending, or
// @Description users carrying the `invited_by` user attribute (written by
// @Description POST /admin/users/invite when that lands in Stage B).
// @Tags        invitations
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} ListInvitationsResponse
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/invitations [get]
func (h *Handler) ListInvitations(c *gin.Context) {
	invs, err := h.service.ListInvitations(c.Request.Context())
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]InvitationResponse, 0, len(invs))
	for _, i := range invs {
		out = append(out, toInvitationResponse(i))
	}
	c.JSON(http.StatusOK, ListInvitationsResponse{Invitations: out, Count: len(out)})
}

// ─── Stage 5.2C — UPDATE handlers ────────────────────────────────────────

// callerSubject returns the authenticated user's `sub` (Keycloak UUID) from
// the gin context, or "" when no identity is present. Used by self-protection
// guards in the service tier.
func callerSubject(c *gin.Context) string {
	if id, ok := auth.IdentityFrom(c); ok && id != nil {
		return id.Subject
	}
	return ""
}

// UpdateUser handles PATCH /admin/users/:id.
//
// @Summary     Update a user (admin)
// @Description Partial update — omitted fields are preserved. Guarded
// @Description against self-disable and last-admin removal. Returns the
// @Description updated user representation.
// @Tags        users
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id   path string                true  "Keycloak sub UUID"
// @Param       body body UpdateUserRequestBody true  "fields to update"
// @Success     200 {object} UserResponse
// @Failure     400 {object} map[string]string "malformed id or body"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "self-disable / last-admin guard"
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /admin/users/{id} [patch]
func (h *Handler) UpdateUser(c *gin.Context) {
	var body UpdateUserRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	targetID := c.Param("id")
	user, err := h.service.UpdateUser(c.Request.Context(), callerSubject(c), targetID, UpdateUserRequest{
		FirstName:     body.FirstName,
		LastName:      body.LastName,
		Email:         body.Email,
		Enabled:       body.Enabled,
		EmailVerified: body.EmailVerified,
	})
	target := audit.Target{Kind: "user", ID: targetID}
	if err == nil && user != nil {
		target.Name = user.Email
	}
	logging.RecordMutation(c, audit.ActionUserUpdated, target, err)
	if err != nil {
		handleError(c, err)
		return
	}
	// Disabling a user invalidates admin authorization for them; refresh the
	// auth-tier live-admin cache so the change takes effect on the next
	// request rather than after the cache TTL. Invalidate on success even
	// when no enabled change happened — cheap, and avoids matching on the
	// optional-pointer to decide.
	h.adminInvalidator.Invalidate(targetID)
	c.JSON(http.StatusOK, toUserResponse(*user))
}

// UpdateRole handles PATCH /admin/roles/:name.
//
// @Summary     Update a role's description (admin)
// @Description Only the description field is mutable. Protected roles
// @Description (admin, user, default-roles-*, offline_access, uma_authorization)
// @Description are rejected with 403.
// @Tags        roles
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       name path string                true "realm role name"
// @Param       body body UpdateRoleRequestBody true "fields to update"
// @Success     200 {object} RoleResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "protected role"
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/roles/{name} [patch]
func (h *Handler) UpdateRole(c *gin.Context) {
	var body UpdateRoleRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	roleName := c.Param("name")
	role, err := h.service.UpdateRole(c.Request.Context(), roleName, UpdateRoleRequest{
		Description: body.Description,
	})
	logging.RecordMutation(c, audit.ActionRoleUpdated,
		audit.Target{Kind: "role", ID: roleName, Name: roleName}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toRoleResponse(*role))
}

// AssignRolesToUser handles POST /admin/users/:id/roles.
//
// @Summary     Assign realm roles to a user (admin)
// @Description Grants the named realm roles. Roles are resolved by name —
// @Description a missing role short-circuits with 404 before any
// @Description partial-assignment happens.
// @Tags        users
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id   path string                  true "Keycloak sub UUID"
// @Param       body body AssignRolesRequestBody  true "role names to grant"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "user or role not found"
// @Failure     502 {object} map[string]string
// @Router      /admin/users/{id}/roles [post]
func (h *Handler) AssignRolesToUser(c *gin.Context) {
	var body AssignRolesRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	targetID := c.Param("id")
	err := h.service.AssignRolesToUser(c.Request.Context(), targetID, body.Roles)
	// Roles travel in Extra so the audit line carries which roles were
	// requested — Target alone (the user UUID) can't express that.
	rolesCopy := append([]string(nil), body.Roles...)
	event := logging.EventFromGin(c, audit.Event{
		Action: audit.ActionUserRolesGranted,
		Target: audit.Target{Kind: "user", ID: targetID},
		Extra:  map[string]any{"roles": rolesCopy},
	})
	if err != nil {
		event.Reason = err.Error()
	}
	audit.Record(c.Request.Context(), event)
	if err != nil {
		handleError(c, err)
		return
	}
	// Role grant may have flipped admin status — drop cached admin check.
	h.adminInvalidator.Invalidate(targetID)
	c.Status(http.StatusNoContent)
}

// UnassignRoleFromUser handles DELETE /admin/users/:id/roles/:name. Removes
// a SINGLE role from the user; matches the natural URL shape.
//
// @Summary     Remove a realm role from a user (admin)
// @Description Guarded: refuses to remove your own admin role; refuses to
// @Description remove the last enabled admin.
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Param       id   path string true "Keycloak sub UUID"
// @Param       name path string true "realm role name to remove"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "self-strip admin / last-admin guard"
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/users/{id}/roles/{name} [delete]
func (h *Handler) UnassignRoleFromUser(c *gin.Context) {
	targetID := c.Param("id")
	roleName := c.Param("name")
	err := h.service.UnassignRolesFromUser(c.Request.Context(), callerSubject(c), targetID, []string{roleName})
	// Target.Name carries the role name so a one-line audit grep tells
	// the full story without needing to crack the Extra map.
	logging.RecordMutation(c, audit.ActionUserRoleRevoked,
		audit.Target{Kind: "user", ID: targetID, Name: roleName}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	// The GAP-1 hot path: revoking admin must stop authorizing the target's
	// in-flight token at the next request, not after the cache TTL.
	h.adminInvalidator.Invalidate(targetID)
	c.Status(http.StatusNoContent)
}

// ResetUserPassword handles POST /admin/users/:id/reset-password. Sends an
// UPDATE_PASSWORD action email.
//
// @Summary     Send a password-reset email (admin)
// @Description Queues UPDATE_PASSWORD as a required action and dispatches
// @Description the action email via Keycloak's execute-actions-email
// @Description endpoint. Requires realm SMTP to be configured (a 502
// @Description usually means it isn't).
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Keycloak sub UUID"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string "SMTP not configured / upstream failure"
// @Router      /admin/users/{id}/reset-password [post]
func (h *Handler) ResetUserPassword(c *gin.Context) {
	targetID := c.Param("id")
	err := h.service.SendResetPasswordEmail(c.Request.Context(), targetID)
	logging.RecordMutation(c, audit.ActionUserPasswordReset,
		audit.Target{Kind: "user", ID: targetID}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// SetUserPassword handles PUT /admin/users/:id/password. Sets the user's
// password directly (no email required). Accepts {"password":"...","temporary":bool}.
//
// @Summary     Set user password (admin)
// @Description Sets the user's Keycloak credential directly. When temporary=true
// @Description the user must change the password on next login.
// @Tags        users
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id   path     string true "Keycloak sub UUID"
// @Param       body body     object true "password payload"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /admin/users/{id}/password [put]
func (h *Handler) SetUserPassword(c *gin.Context) {
	targetID := c.Param("id")
	var body struct {
		Password  string `json:"password"`
		Temporary bool   `json:"temporary"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	err := h.service.SetUserPassword(c.Request.Context(), targetID, body.Password, body.Temporary)
	logging.RecordMutation(c, audit.ActionUserPasswordReset,
		audit.Target{Kind: "user", ID: targetID}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ResendInvitation handles POST /admin/invitations/:id/resend.
//
// @Summary     Re-send an invitation email (admin)
// @Description Idempotent — re-dispatches the VERIFY_EMAIL + UPDATE_PASSWORD
// @Description action email. Returns the invitation as it stands now.
// @Tags        invitations
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Invitation id (= Keycloak user UUID)"
// @Success     200 {object} InvitationResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/invitations/{id}/resend [post]
func (h *Handler) ResendInvitation(c *gin.Context) {
	targetID := c.Param("id")
	inv, err := h.service.ResendInvitation(c.Request.Context(), targetID)
	target := audit.Target{Kind: "invitation", ID: targetID}
	if err == nil && inv != nil {
		target.Name = inv.Email
	}
	logging.RecordMutation(c, audit.ActionInvitationResent, target, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, toInvitationResponse(*inv))
}

// ─── Stage 5.2D — DELETE handlers ────────────────────────────────────────

// DeleteUser handles DELETE /admin/users/:id.
//
// @Summary     Delete a user (admin)
// @Description Guarded: refuses to delete your own account; refuses to
// @Description delete the last enabled admin.
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Keycloak sub UUID"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "self-delete / last-admin guard"
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/users/{id} [delete]
func (h *Handler) DeleteUser(c *gin.Context) {
	targetID := c.Param("id")
	err := h.service.DeleteUser(c.Request.Context(), callerSubject(c), targetID)
	logging.RecordMutation(c, audit.ActionUserDeleted,
		audit.Target{Kind: "user", ID: targetID}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	h.adminInvalidator.Invalidate(targetID)
	c.Status(http.StatusNoContent)
}

// DeleteRole handles DELETE /admin/roles/:name.
//
// @Summary     Delete a realm role (admin)
// @Description Protected roles (admin, user, default-roles-*,
// @Description offline_access, uma_authorization) are rejected with 403.
// @Tags        roles
// @Produce     json
// @Security    BearerAuth
// @Param       name path string true "realm role name"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "protected role"
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/roles/{name} [delete]
func (h *Handler) DeleteRole(c *gin.Context) {
	roleName := c.Param("name")
	err := h.service.DeleteRole(c.Request.Context(), roleName)
	logging.RecordMutation(c, audit.ActionRoleDeleted,
		audit.Target{Kind: "role", ID: roleName, Name: roleName}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	// Realm-role deletion is rare but invalidates the admin set globally
	// (the role itself or any role that composes admin could be gone) — wipe
	// the whole cache rather than try to enumerate affected subjects.
	h.adminInvalidator.InvalidateAll()
	c.Status(http.StatusNoContent)
}

// DeleteSession handles DELETE /admin/sessions/:id.
//
// @Summary     Revoke a single session (admin)
// @Tags        sessions
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "session UUID"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/sessions/{id} [delete]
func (h *Handler) DeleteSession(c *gin.Context) {
	sessionID := c.Param("id")
	err := h.service.DeleteSession(c.Request.Context(), sessionID)
	logging.RecordMutation(c, audit.ActionSessionRevoked,
		audit.Target{Kind: "session", ID: sessionID}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// LogoutUserSessions handles DELETE /admin/users/:id/sessions. Revokes
// every active session for a user.
//
// @Summary     Revoke every session of a user (admin)
// @Description Logs the target user out of every client. Not blocked for
// @Description self-logout; an admin doing this to themselves invalidates
// @Description their current token too.
// @Tags        sessions
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Keycloak sub UUID"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/users/{id}/sessions [delete]
func (h *Handler) LogoutUserSessions(c *gin.Context) {
	targetID := c.Param("id")
	err := h.service.LogoutUserSessions(c.Request.Context(), targetID)
	logging.RecordMutation(c, audit.ActionUserSessionsLoggedOut,
		audit.Target{Kind: "user", ID: targetID}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// DeleteInvitation handles DELETE /admin/invitations/:id. An invitation is
// a user in invited-but-incomplete state; deletion removes the user.
//
// @Summary     Revoke an invitation (admin)
// @Description Deletes the underlying Keycloak user. Guarded against
// @Description self-delete.
// @Tags        invitations
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Invitation id (= Keycloak user UUID)"
// @Success     204
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Router      /admin/invitations/{id} [delete]
func (h *Handler) DeleteInvitation(c *gin.Context) {
	targetID := c.Param("id")
	err := h.service.DeleteInvitation(c.Request.Context(), callerSubject(c), targetID)
	logging.RecordMutation(c, audit.ActionInvitationRevoked,
		audit.Target{Kind: "invitation", ID: targetID}, err)
	if err != nil {
		handleError(c, err)
		return
	}
	// Invitations are users — deleting one removes the underlying subject.
	h.adminInvalidator.Invalidate(targetID)
	c.Status(http.StatusNoContent)
}

// handleError maps service-layer sentinel errors to HTTP status codes.
// Centralized so every endpoint behaves identically.
func handleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, ErrBadRequest):
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
	case errors.Is(err, ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	case errors.Is(err, ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "conflict"})
	case errors.Is(err, ErrNotConfigured):
		log.Error("identity: " + err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "identity management not configured"})
	case errors.Is(err, ErrAdminAPIUnavailable):
		log.Error("identity: upstream keycloak unavailable: " + err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream identity provider unavailable"})
	default:
		log.Error("identity: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}
