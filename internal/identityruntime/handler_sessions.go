package identityruntime

import (
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/gin-gonic/gin"
)

// Sessions and password operations, scoped to a workspace.

// ListSessions handles GET /v1/workspaces/{workspace_id}/sessions.
//
// @Summary     List active sessions across a workspace's realm
// @Description Aggregates sessions client by client, so a realm with many
// @Description clients pays a per-client round trip. Realm-wide, not per-user —
// @Description for one user's sessions use the user-scoped route.
// @Description
// @Description **Required scope (project credentials):** `sessions:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} identity.ListSessionsResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, workspace_connection_missing, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/sessions [get]
func (h *Handler) ListSessions(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}

	sessions, err := sc.service.ListSessions(c.Request.Context())
	if err != nil {
		respondError(c, translateIdentityError(err, kindSession))
		return
	}
	c.JSON(http.StatusOK, sessionsResponse(sessions))
}

// DeleteSession handles DELETE /v1/workspaces/{workspace_id}/sessions/{session_id}.
//
// @Summary     Revoke a single session in a workspace
// @Description **Required scope (project credentials):** `sessions:revoke`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       session_id   path string true "session UUID"
// @Success     204
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_session_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, session_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/sessions/{session_id} [delete]
func (h *Handler) DeleteSession(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	sessionID, ok := pathSessionID(c)
	if !ok {
		return
	}

	err := sc.service.DeleteSession(c.Request.Context(), sessionID)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionSessionRevoked,
		audit.Target{Kind: "session", ID: sessionID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindSession))
		return
	}
	c.Status(http.StatusNoContent)
}

// ListUserSessions handles GET /v1/workspaces/{workspace_id}/users/{user_id}/sessions.
//
// @Summary     List one user's sessions in a workspace
// @Description **Required scope (project credentials):** `sessions:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Success     200 {object} identity.ListSessionsResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id}/sessions [get]
func (h *Handler) ListUserSessions(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}

	sessions, err := sc.service.ListUserSessions(c.Request.Context(), userID)
	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.JSON(http.StatusOK, sessionsResponse(sessions))
}

// LogoutUserSessions handles
// DELETE /v1/workspaces/{workspace_id}/users/{user_id}/sessions.
//
// @Summary     Revoke all of a user's sessions in a workspace
// @Description Deliberately NOT guarded against self-logout: an admin logging
// @Description themselves out of other browsers is a valid recovery action, and
// @Description the worst case is they invalidate their own token too.
// @Description
// @Description **Required scope (project credentials):** `sessions:revoke`.
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
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id}/sessions [delete]
func (h *Handler) LogoutUserSessions(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}

	err := sc.service.LogoutUserSessions(c.Request.Context(), userID)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserSessionsLoggedOut,
		audit.Target{Kind: "user", ID: userID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Password operations
// ---------------------------------------------------------------------------

// ResetUserPassword handles
// POST /v1/workspaces/{workspace_id}/users/{user_id}/reset-password.
//
// @Summary     Send a password-reset action email to a user in a workspace
// @Description Dispatches Keycloak's UPDATE_PASSWORD action email. This is the
// @Description EMAIL flow, and it requires SMTP configured on the workspace's
// @Description realm — an unconfigured realm surfaces as `provider_unavailable`.
// @Description To set a password directly instead, use PUT .../password.
// @Description
// @Description The two are exposed as separate routes on purpose: they have
// @Description different prerequisites and different outcomes, and hiding that
// @Description behind one endpoint with a flag would make failures unreadable.
// @Description
// @Description **Required scope (project credentials):** `users:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Success     202
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id}/reset-password [post]
func (h *Handler) ResetUserPassword(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}

	err := sc.service.SendResetPasswordEmail(c.Request.Context(), userID)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserPasswordReset,
		audit.Target{Kind: "user", ID: userID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	// 202: Keycloak has accepted the mail for delivery. Whether it arrives is
	// between the realm's SMTP server and the user's inbox, and claiming 200
	// would assert something this API cannot know.
	c.Status(http.StatusAccepted)
}

// SetUserPassword handles
// PUT /v1/workspaces/{workspace_id}/users/{user_id}/password.
//
// @Summary     Set a user's password directly in a workspace
// @Description Sets the password without sending email. With
// @Description `temporary: true` Keycloak forces a change on next login.
// @Description Minimum 8 characters here; the realm's own password policy
// @Description applies on top and may reject more.
// @Description
// @Description The password is never echoed, never logged, and never part of
// @Description the audit event this emits.
// @Tags        workspace-identity
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       user_id      path string true "Keycloak sub UUID"
// @Param       body body SetPasswordRequest true "the new password"
// @Success     204
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_user_id, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, user_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/users/{user_id}/password [put]
func (h *Handler) SetUserPassword(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	userID, ok := pathUserID(c)
	if !ok {
		return
	}
	var req SetPasswordRequest
	if !bind(c, &req) {
		return
	}

	err := sc.service.SetUserPassword(c.Request.Context(), userID, req.Password, req.Temporary)
	// Target carries the user id only. Neither the password nor its length
	// goes into the event — length is a meaningful hint to anyone reading the
	// log later.
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserPasswordReset,
		audit.Target{Kind: "user", ID: userID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindUser))
		return
	}
	c.Status(http.StatusNoContent)
}

// sessionsResponse renders sessions with internal/identity's own wire types,
// so /admin/sessions and the workspace-scoped route cannot disagree.
func sessionsResponse(sessions []identity.Session) identity.ListSessionsResponse {
	out := make([]identity.SessionResponse, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, identity.NewSessionResponse(s))
	}
	return identity.ListSessionsResponse{Sessions: out, Count: len(out)}
}
