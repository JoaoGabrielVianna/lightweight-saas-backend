package identityruntime

import (
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/gin-gonic/gin"
)

// Invitations, scoped to a workspace.
//
// ─── What an invitation is here ─────────────────────────────────────────────
//
// Keycloak has no invitation resource. An "invitation" in this API is a DERIVED
// view over users in an invited-but-incomplete state: pending required actions,
// or carrying the `invited_by` / `expires_at` user attributes. The id in these
// paths IS a Keycloak user id, and deleting an invitation deletes the user.
//
// That model is preserved verbatim from /admin/*, not redesigned, and the route
// shape is honest about it only if you know the above — which is why it is
// stated here, in the OpenAPI descriptions, and in
// docs/WORKSPACE_IDENTITY_API.md rather than left for someone to discover from
// a 404. Two consequences a client must plan for:
//
//   - `GET .../invitations` returns users, so a user who completed their
//     required actions stops appearing. Nothing was deleted.
//   - `DELETE .../invitations/{id}` and `DELETE .../users/{id}` remove the same
//     row. The invitation route omits the last-admin guard because an invited
//     user cannot yet hold admin; if you point it at a completed admin user it
//     will still refuse self-deletion but not last-admin removal. Prefer the
//     user route for users who have accepted.
//
// Redesigning this into a first-class invitation resource is a product
// decision, not a migration, and is deliberately out of scope.

// ListInvitations handles GET /v1/workspaces/{workspace_id}/invitations.
//
// @Summary     List a workspace's pending invitations
// @Description Invitations are DERIVED from user state — Keycloak has no
// @Description invitation resource. A user with pending required actions or
// @Description invitation attributes appears here; once they complete those
// @Description actions they stop appearing, without anything being deleted.
// @Description
// @Description `status` is computed: pending, accepted, expired or revoked.
// @Description
// @Description **Required scope (project credentials):** `invitations:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} identity.ListInvitationsResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, workspace_connection_missing, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/invitations [get]
func (h *Handler) ListInvitations(c *gin.Context) {
	sc, ok := h.read(c)
	if !ok {
		return
	}

	invitations, err := sc.service.ListInvitations(c.Request.Context())
	if err != nil {
		respondError(c, translateIdentityError(err, kindInvitation))
		return
	}

	out := make([]identity.InvitationResponse, 0, len(invitations))
	for _, i := range invitations {
		out = append(out, identity.NewInvitationResponse(i))
	}
	c.JSON(http.StatusOK, identity.ListInvitationsResponse{Invitations: out, Count: len(out)})
}

// CreateInvitation handles POST /v1/workspaces/{workspace_id}/invitations.
//
// @Summary     Invite a user to a workspace's realm
// @Description Provisions a user in invited-but-incomplete state, assigns the
// @Description requested realm roles, and dispatches Keycloak's action email.
// @Description
// @Description REQUIRES SMTP configured on the workspace's realm. Without it
// @Description the email dispatch fails, and the half-provisioned user is
// @Description removed so the same address can be retried — you get an error,
// @Description not a silent half-invitation. Installations without SMTP should
// @Description use POST .../users instead, which sets a temporary password and
// @Description sends nothing.
// @Description
// @Description **Required scope (project credentials):** `invitations:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       body body CreateInvitationRequest true "invitation to send"
// @Success     201 {object} identity.InvitationResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, role_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, conflict, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable (includes SMTP not configured on the realm)"
// @Router      /v1/workspaces/{workspace_id}/invitations [post]
func (h *Handler) CreateInvitation(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	var req CreateInvitationRequest
	if !bind(c, &req) {
		return
	}

	// defaultInvitedBy: the caller's own identity, used only when the body
	// does not name someone. Same precedence /admin/invitations uses.
	invitation, err := sc.service.CreateInvitation(c.Request.Context(), identity.CreateInvitationRequest{
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Roles:     req.Roles,
		ExpiresAt: req.ExpiresAt,
		InvitedBy: req.InvitedBy,
	}, callerLabel(c))

	target := audit.Target{Kind: "invitation", Name: req.Email}
	if invitation != nil {
		target.ID = invitation.ID
	}
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionInvitationCreated, target, err)
	// Two events for one call, matching /admin/invitations exactly. Inviting
	// is also the act of creating a Keycloak user, and a consumer watching
	// `user.created` to answer "who got access to this realm" must see it
	// here too — otherwise invited users are invisible to that question.
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionUserCreated,
		audit.Target{Kind: "user", ID: target.ID, Name: req.Email}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindInvitation))
		return
	}
	c.JSON(http.StatusCreated, identity.NewInvitationResponse(*invitation))
}

// ResendInvitation handles
// POST /v1/workspaces/{workspace_id}/invitations/{invitation_id}/resend.
//
// @Summary     Re-send a workspace invitation's action email
// @Description Only the still-pending actions are re-dispatched, so a user who
// @Description already verified their email is not asked to do it again.
// @Description
// @Description An ACCEPTED invitation (nothing left to do) and a REVOKED one
// @Description (user disabled) both return `conflict` — the first has nothing
// @Description to resend, the second needs the user re-enabled first.
// @Description
// @Description **Required scope (project credentials):** `invitations:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id  path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       invitation_id path string true "the invited user's Keycloak sub UUID"
// @Success     200 {object} identity.InvitationResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only, workspace_mismatch, insufficient_scope, role_privileged"
// @Failure     404 {object} ErrorResponse "workspace_not_found, invitation_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, conflict, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable (includes SMTP not configured on the realm)"
// @Router      /v1/workspaces/{workspace_id}/invitations/{invitation_id}/resend [post]
func (h *Handler) ResendInvitation(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	invitationID, ok := pathInvitationID(c)
	if !ok {
		return
	}

	invitation, err := sc.service.ResendInvitation(c.Request.Context(), invitationID)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionInvitationResent,
		audit.Target{Kind: "invitation", ID: invitationID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindInvitation))
		return
	}
	c.JSON(http.StatusOK, identity.NewInvitationResponse(*invitation))
}

// DeleteInvitation handles
// DELETE /v1/workspaces/{workspace_id}/invitations/{invitation_id}.
//
// @Summary     Revoke a workspace invitation
// @Description An invitation IS a user, so this deletes the user. Self-deletion
// @Description is refused; the last-admin guard does NOT apply here, because an
// @Description invited user cannot yet hold admin. To delete a user who has
// @Description accepted, use DELETE .../users/{user_id}, which is guarded.
// @Description
// @Description **Required scope (project credentials):** `invitations:write`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Tags        workspace-identity
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id  path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       invitation_id path string true "the invited user's Keycloak sub UUID"
// @Success     204
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "caller_forbidden"
// @Failure     404 {object} ErrorResponse "workspace_not_found, invitation_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, connection_read_only, provider_forbidden"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Failure     502 {object} ErrorResponse "provider_unavailable"
// @Router      /v1/workspaces/{workspace_id}/invitations/{invitation_id} [delete]
func (h *Handler) DeleteInvitation(c *gin.Context) {
	sc, ok := h.write(c)
	if !ok {
		return
	}
	invitationID, ok := pathInvitationID(c)
	if !ok {
		return
	}

	err := sc.service.DeleteInvitation(c.Request.Context(), callerSubject(c), invitationID)
	logging.RecordWorkspaceMutation(c, sc.workspaceID(), audit.ActionInvitationRevoked,
		audit.Target{Kind: "invitation", ID: invitationID}, err)

	if err != nil {
		respondError(c, translateIdentityError(err, kindInvitation))
		return
	}
	c.Status(http.StatusNoContent)
}

// callerLabel is the identifier recorded as `invited_by` when the request body
// does not name one: email when the token carries it, otherwise the raw
// subject. Deliberately the same precedence /admin/invitations applies, so an
// invitation's stored attribution does not depend on which surface sent it.
func callerLabel(c *gin.Context) string {
	id, ok := auth.IdentityFrom(c)
	if !ok || id == nil {
		return ""
	}
	if id.Email != "" {
		return id.Email
	}
	return id.Subject
}
