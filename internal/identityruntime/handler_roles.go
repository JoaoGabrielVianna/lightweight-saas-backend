package identityruntime

import (
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/gin-gonic/gin"
)

// Realm roles, and the role memberships of a user, scoped to a workspace.

// ListRoles handles GET /v1/workspaces/{workspace_id}/roles.
//
// @Summary     List a workspace's realm roles
// @Description Returns the realm roles from the Keycloak realm that the
// @Description workspace's ACTIVE connection points at. Roles Keycloak manages
// @Description itself are returned with `builtin: true`.
// @Description
// @Description **Required scope (project credentials):** `roles:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} identity.ListRolesResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, workspace_connection_missing, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/roles [get]
func (h *Handler) ListRoles(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}

	roles, err := sc.service.ListRoles(c.Request.Context())
	if err != nil {
		respondError(c, translateIdentityError(err, kindRole))
		return
	}

	out := make([]identity.RoleResponse, 0, len(roles))
	for _, r := range roles {
		out = append(out, identity.NewRoleResponse(r))
	}
	c.JSON(http.StatusOK, identity.ListRolesResponse{Roles: out, Count: len(out)})
}

// CreateRole handles POST /v1/workspaces/{workspace_id}/roles.
//
// @Summary     Create a realm role in a workspace
// @Description Creates a realm role in the Keycloak realm that the workspace's
// @Description ACTIVE connection points at. The role appears in that realm and
// @Description in no other.
// @Description
// @Description Names are trimmed and lowercased. Names the platform or Keycloak
// @Description manages (`admin`, `user`, `offline_access`, `uma_authorization`,
// @Description `default-roles-*`) are refused with `role_reserved`.
// @Description
// @Description **Required scope (project credentials):** `roles:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       body body CreateRoleRequest true "role to create"
// @Success     201 {object} identity.RoleResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, role_already_exists, role_reserved, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/roles [post]
func (h *Handler) CreateRole(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	var req CreateRoleRequest
	if !bind(c, &req) {
		return
	}

	role, err := sc.service.CreateRole(c.Request.Context(), identity.CreateRoleRequest{
		Name:        req.Name,
		Description: req.Description,
	})
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionRoleCreated,
		audit.Target{Kind: "role", ID: req.Name, Name: req.Name}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindRole))
		return
	}
	c.JSON(http.StatusCreated, identity.NewRoleResponse(*role))
}

// GetRole handles GET /v1/workspaces/{workspace_id}/roles/{role_name}.
//
// @Summary     Get a realm role in a workspace
// @Description **Required scope (project credentials):** `roles:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       role_name    path string true "realm role name" example(billing-admin)
// @Success     200 {object} identity.RoleResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_role_name"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/roles/{role_name} [get]
func (h *Handler) GetRole(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}
	name, ok := pathRoleName(c)
	if !ok {
		return
	}

	role, err := sc.service.GetRole(c.Request.Context(), name)
	if err != nil {
		respondError(c, translateIdentityError(err, kindRole))
		return
	}
	c.JSON(http.StatusOK, identity.NewRoleResponse(*role))
}

// UpdateRole handles PATCH /v1/workspaces/{workspace_id}/roles/{role_name}.
//
// @Summary     Update a realm role's description in a workspace
// @Description `description` is the only mutable field. Renaming is out of
// @Description scope: it would require rewriting every role-mapping that
// @Description references the old name. Protected roles return `role_reserved`.
// @Description
// @Description **Required scope (project credentials):** `roles:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       role_name    path string true "realm role name" example(billing-admin)
// @Param       body body UpdateRoleRequest true "fields to change"
// @Success     200 {object} identity.RoleResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_role_name, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, role_reserved, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/roles/{role_name} [patch]
func (h *Handler) UpdateRole(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	name, ok := pathRoleName(c)
	if !ok {
		return
	}
	var req UpdateRoleRequest
	if !bind(c, &req) {
		return
	}

	role, err := sc.service.UpdateRole(c.Request.Context(), name, identity.UpdateRoleRequest{
		Description: req.Description,
	})
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionRoleUpdated,
		audit.Target{Kind: "role", ID: name, Name: name}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindRole))
		return
	}
	c.JSON(http.StatusOK, identity.NewRoleResponse(*role))
}

// DeleteRole handles DELETE /v1/workspaces/{workspace_id}/roles/{role_name}.
//
// @Summary     Delete a realm role in a workspace
// @Description Protected roles return `role_reserved`.
// @Description
// @Description **Required scope (project credentials):** `roles:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       role_name    path string true "realm role name" example(billing-admin)
// @Success     204
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_role_name"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, role_reserved, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/roles/{role_name} [delete]
func (h *Handler) DeleteRole(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	name, ok := pathRoleName(c)
	if !ok {
		return
	}

	err := sc.service.DeleteRole(c.Request.Context(), name)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionRoleDeleted,
		audit.Target{Kind: "role", ID: name, Name: name}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindRole))
		return
	}
	c.Status(http.StatusNoContent)
}

// ListRoleUsers handles GET /v1/workspaces/{workspace_id}/roles/{role_name}/users.
//
// @Summary     List the users carrying a realm role in a workspace
// @Description Pages through the full membership rather than returning
// @Description Keycloak's default first 100 — the last-admin guard depends on
// @Description seeing the complete set.
// @Description
// @Description **Required scope (project credentials):** `roles:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       role_name    path string true "realm role name" example(billing-admin)
// @Success     200 {object} identity.ListUsersResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_role_name"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/roles/{role_name}/users [get]
func (h *Handler) ListRoleUsers(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}
	name, ok := pathRoleName(c)
	if !ok {
		return
	}

	users, err := sc.service.ListUsersByRole(c.Request.Context(), name)
	if err != nil {
		respondError(c, translateIdentityError(err, kindRole))
		return
	}

	out := make([]identity.UserResponse, 0, len(users))
	for _, u := range users {
		out = append(out, identity.NewUserResponse(u))
	}
	// First/Max are zero: this endpoint returns the COMPLETE membership, not
	// a page, so reporting a page size would describe a pagination that does
	// not exist. Count is the honest field here.
	c.JSON(http.StatusOK, identity.ListUsersResponse{Users: out, Count: len(out)})
}

// ---------------------------------------------------------------------------
// A user's roles
// ---------------------------------------------------------------------------

// ListUserRoles handles GET /v1/workspaces/{workspace_id}/users/{user_id}/roles.
//
// @Summary     List a user's realm roles in a workspace
// @Description **Required scope (project credentials):** `roles:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Success     200 {object} identity.ListRolesResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id}/roles [get]
func (h *Handler) ListUserRoles(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}

	roles, err := sc.service.ListUserRoles(c.Request.Context(), userID)
	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}

	out := make([]identity.RoleResponse, 0, len(roles))
	for _, r := range roles {
		out = append(out, identity.NewRoleResponse(r))
	}
	c.JSON(http.StatusOK, identity.ListRolesResponse{Roles: out, Count: len(out)})
}

// AssignRolesToUser handles POST /v1/workspaces/{workspace_id}/users/{user_id}/roles.
//
// @Summary     Grant realm roles to a user in a workspace
// @Description Roles are resolved by name; a missing role short-circuits with
// @Description `role_not_found` before any partial assignment happens.
// @Description
// @Description **Required scope (project credentials):** `roles:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Param       body body AssignRolesRequest true "role names to grant"
// @Success     204
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id}/roles [post]
func (h *Handler) AssignRolesToUser(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}
	var req AssignRolesRequest
	if !bind(c, &req) {
		return
	}
	// A machine credential may not hand out administrative roles. Checked
	// before the provider is contacted, so a refused grant leaves no trace in
	// the realm. See role_guard.go for why assignment needed a guard the
	// service does not already have.
	if !h.guardPrivilegedRoles(c, req.Roles...) {
		return
	}

	err := sc.service.AssignRolesToUser(c.Request.Context(), userID, req.Roles)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserRolesGranted,
		audit.Target{Kind: "user", ID: userID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.Status(http.StatusNoContent)
}

// UnassignRoleFromUser handles
// DELETE /v1/workspaces/{workspace_id}/users/{user_id}/roles/{role_name}.
//
// @Summary     Revoke a realm role from a user in a workspace
// @Description Guarded against removing your own `admin` role and against
// @Description removing it from the realm's last enabled admin; both return
// @Description `caller_forbidden`.
// @Description
// @Description **Required scope (project credentials):** `roles:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Param       role_name    path string true "realm role name" example(billing-admin)
// @Success     204
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id, invalid_role_name"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "caller_forbidden"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id}/roles/{role_name} [delete]
func (h *Handler) UnassignRoleFromUser(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}
	name, ok := pathRoleName(c)
	if !ok {
		return
	}
	// Revocation is guarded as well as granting: a machine able to strip
	// `admin` could lock every operator out of the realm it administers.
	if !h.guardPrivilegedRoles(c, name) {
		return
	}

	err := sc.service.UnassignRolesFromUser(c.Request.Context(), callerSubject(c), userID, []string{name})
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserRoleRevoked,
		audit.Target{Kind: "user", ID: userID, Name: name}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.Status(http.StatusNoContent)
}
