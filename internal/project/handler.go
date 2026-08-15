package project

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

// Handler is the project management HTTP surface.
//
// EVERY route here is operator-only, classified as such in the authz registry
// and enforced by the /v1 group's Authorize middleware. A credential able to
// mint credentials would make revocation meaningless — revoke one, and it has
// already issued another — so this is the one boundary in the product that no
// scope can cross.
//
// Authentication and authorization are not performed here for the same reason
// the workspace and connection handlers do not perform them: a handler that
// also checked would be a second, divergeable copy of the rule.
type Handler struct {
	service *Service
	now     func() time.Time
}

// NewHandler constructs a Handler. Returns nil when the service is nil, so the
// composition root omits the routes rather than mounting handlers that panic.
func NewHandler(service *Service) *Handler {
	if service == nil {
		return nil
	}
	return &Handler{service: service, now: time.Now}
}

// ─── Projects ───────────────────────────────────────────────────────────────

// List handles GET /v1/workspaces/{workspace_id}/projects.
//
// @Summary     List a workspace's projects
// @Description Returns every project in the workspace, active and archived,
// @Description with a count of the credentials that can currently authenticate.
// @Description Archived projects are included because this listing backs a
// @Description management screen: hiding them would leave no way to confirm an
// @Description archive happened.
// @Description
// @Description **Operator only.** No project credential can reach this route,
// @Description whatever scopes it holds.
// @Tags        projects
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} ListProjectsResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects [get]
func (h *Handler) List(c *gin.Context) {
	items, counts, err := h.service.List(c.Request.Context(), c.Param("workspace_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectListResponse(items, counts))
}

// Create handles POST /v1/workspaces/{workspace_id}/projects.
//
// @Summary     Create a project
// @Description Creates an active project bound PERMANENTLY to this workspace.
// @Description The binding cannot be changed: it is the authorization boundary
// @Description every credential of this project is confined to. If another
// @Description workspace needs API access, create a project there.
// @Description
// @Description Names are unique per workspace, compared case-insensitively.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       body body CreateProjectRequest true "project to create"
// @Success     201 {object} ProjectResponse
// @Failure     400 {object} ErrorResponse "project_name_required, invalid_workspace_id, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived, project_name_taken"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects [post]
func (h *Handler) Create(c *gin.Context) {
	var req CreateProjectRequest
	if !bind(c, &req) {
		return
	}

	// Seeded with the path workspace and the requested name so a FAILURE is
	// still attributable; the service overwrites the target with the row it
	// actually inserted, inside the transaction.
	ev := logging.ControlPlaneEvent(c, audit.ActionProjectCreated)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: TargetKindProject, Name: req.Name}

	p, err := h.service.Create(c.Request.Context(), c.Param("workspace_id"), req.Name, ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toProjectResponse(p, 0))
}

// Get handles GET /v1/workspaces/{workspace_id}/projects/{project_id}.
//
// @Summary     Get a project
// @Description A project belonging to a different workspace reads as not found,
// @Description so an id cannot be used to confirm another workspace's contents.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       project_id   path string true "project id"   example(prj_7c9e6679-7425-40de-944b-e07fc1f90ae7)
// @Success     200 {object} ProjectResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_project_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found, project_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects/{project_id} [get]
func (h *Handler) Get(c *gin.Context) {
	p, err := h.service.Get(c.Request.Context(), c.Param("workspace_id"), c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}

	active, err := h.service.ActiveCredentialCount(c.Request.Context(), p)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectResponse(p, active))
}

// Update handles PATCH /v1/workspaces/{workspace_id}/projects/{project_id}.
//
// @Summary     Rename a project
// @Description Name is the only mutable field. `workspace_id` is declared in
// @Description the body solely so this endpoint can REFUSE it: moving a project
// @Description between workspaces would silently repoint every live credential
// @Description at a different realm, which is the one thing the authorization
// @Description model must never allow.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       project_id   path string true "project id"   example(prj_7c9e6679-7425-40de-944b-e07fc1f90ae7)
// @Param       body body UpdateProjectRequest true "fields to change"
// @Success     200 {object} ProjectResponse
// @Failure     400 {object} ErrorResponse "project_name_required, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found, project_not_found"
// @Failure     409 {object} ErrorResponse "project_archived, project_name_taken"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects/{project_id} [patch]
func (h *Handler) Update(c *gin.Context) {
	var req UpdateProjectRequest
	if !bind(c, &req) {
		return
	}
	// Refusing loudly rather than ignoring: a client that believed it moved a
	// project and got a 200 would only discover otherwise much later, and the
	// whole authorization model rests on that binding never moving.
	if req.WorkspaceID != nil {
		respondError(c, immutableFieldError("workspace_id"))
		return
	}
	if req.Status != nil {
		respondError(c, immutableFieldError("status"))
		return
	}
	if req.Name == nil {
		respondError(c, ErrNameRequired)
		return
	}

	ev := logging.ControlPlaneEvent(c, audit.ActionProjectRenamed)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: TargetKindProject, ID: c.Param("project_id"), Name: *req.Name}

	p, err := h.service.Rename(c.Request.Context(), c.Param("workspace_id"), c.Param("project_id"), *req.Name, ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}

	active, err := h.service.ActiveCredentialCount(c.Request.Context(), p)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectResponse(p, active))
}

// Archive handles POST /v1/workspaces/{workspace_id}/projects/{project_id}/archive.
//
// @Summary     Archive a project
// @Description Freezes the project. EVERY credential it holds stops
// @Description authenticating immediately — authentication reads the project's
// @Description status in the same lookup as the credential, so this is one
// @Description atomic kill switch rather than a loop over keys that could
// @Description half-finish.
// @Description
// @Description Idempotent: archiving an already-archived project succeeds.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       project_id   path string true "project id"   example(prj_7c9e6679-7425-40de-944b-e07fc1f90ae7)
// @Success     200 {object} ProjectResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_project_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found, project_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects/{project_id}/archive [post]
func (h *Handler) Archive(c *gin.Context) {
	ev := logging.ControlPlaneEvent(c, audit.ActionProjectArchived)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: TargetKindProject, ID: c.Param("project_id")}

	p, err := h.service.Archive(c.Request.Context(), c.Param("workspace_id"), c.Param("project_id"), ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectResponse(p, 0))
}

// ─── Credentials ────────────────────────────────────────────────────────────

// ListCredentials handles GET .../projects/{project_id}/credentials.
//
// @Summary     List a project's credentials
// @Description Returns credential METADATA. The secret is never included, and
// @Description no endpoint can return it: only a SHA-256 digest is stored, so
// @Description the plaintext does not exist anywhere in this system after the
// @Description create call returned it.
// @Description
// @Description Revoked credentials are included: the list is the trail an
// @Description operator reads when working out which key a deployment still
// @Description holds.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       project_id   path string true "project id"   example(prj_7c9e6679-7425-40de-944b-e07fc1f90ae7)
// @Success     200 {object} ListCredentialsResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_project_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found, project_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects/{project_id}/credentials [get]
func (h *Handler) ListCredentials(c *gin.Context) {
	items, err := h.service.ListCredentials(c.Request.Context(), c.Param("workspace_id"), c.Param("project_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCredentialListResponse(items, c.Param("project_id"), h.now().UTC()))
}

// CreateCredential handles POST .../projects/{project_id}/credentials.
//
// @Summary     Create a project credential
// @Description Mints a machine credential and returns the secret **once**.
// @Description There is no endpoint that can show it again, and adding one
// @Description would be impossible without changing what is stored: only a
// @Description SHA-256 digest of the secret is persisted.
// @Description
// @Description Scopes are explicit and non-empty. Nothing is granted by
// @Description default, and an empty list is rejected rather than treated as
// @Description "no permissions" — a credential that authenticates and can do
// @Description nothing is a configuration mistake worth reporting at creation.
// @Description
// @Description A project may hold at most 10 active credentials. Rotation is
// @Description create-new, deploy, revoke-old.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       project_id   path string true "project id"   example(prj_7c9e6679-7425-40de-944b-e07fc1f90ae7)
// @Param       body body CreateCredentialRequest true "credential to create"
// @Success     201 {object} CreateCredentialResponse
// @Failure     400 {object} ErrorResponse "credential_label_required, invalid_scope, invalid_request"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found, project_not_found"
// @Failure     409 {object} ErrorResponse "project_archived, credential_limit_reached"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects/{project_id}/credentials [post]
func (h *Handler) CreateCredential(c *gin.Context) {
	var req CreateCredentialRequest
	if !bind(c, &req) {
		return
	}

	// The audit target names the label and the scopes granted, and NEVER the
	// secret or its prefix beyond the credential's own public id. Extra is the
	// only free-text field on the event, so this is the one place a credential
	// could plausibly reach a log — and `secret` below is deliberately not in
	// scope until after this event is built.
	ev := logging.ControlPlaneEvent(c, audit.ActionCredentialCreated)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: TargetKindCredential, Name: req.Label}
	ev.Extra = map[string]any{"scopes": req.Scopes}

	cred, secret, err := h.service.CreateCredential(c.Request.Context(),
		c.Param("workspace_id"), c.Param("project_id"), CreateCredentialInput{
			Label:     req.Label,
			Scopes:    req.Scopes,
			ExpiresAt: req.ExpiresAt,
			CreatedBy: operatorSubject(c),
		}, ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, CreateCredentialResponse{
		Credential: toCredentialResponse(cred, c.Param("project_id"), h.now().UTC()),
		Secret:     secret,
	})
}

// RevokeCredential handles POST .../credentials/{credential_id}/revoke.
//
// @Summary     Revoke a project credential
// @Description Takes effect immediately: authentication reads the credential
// @Description row on every request and there is no cache to invalidate. A
// @Description request already in flight completes — it was authorized when it
// @Description started — but the next one fails.
// @Description
// @Description Revoking an already-revoked credential is a conflict rather than
// @Description a silent success, so an operator learns that someone else got
// @Description there first.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id  path string true "workspace id"  example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       project_id    path string true "project id"    example(prj_7c9e6679-7425-40de-944b-e07fc1f90ae7)
// @Param       credential_id path string true "credential id" example(key_9b2f4c1a-1111-4222-8333-444455556666)
// @Success     200 {object} CredentialResponse
// @Failure     400 {object} ErrorResponse "invalid_project_id, invalid_credential_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Failure     404 {object} ErrorResponse "workspace_not_found, project_not_found, credential_not_found"
// @Failure     409 {object} ErrorResponse "credential_already_revoked"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/projects/{project_id}/credentials/{credential_id}/revoke [post]
func (h *Handler) RevokeCredential(c *gin.Context) {
	ev := logging.ControlPlaneEvent(c, audit.ActionCredentialRevoked)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: TargetKindCredential, ID: c.Param("credential_id")}

	cred, err := h.service.RevokeCredential(c.Request.Context(),
		c.Param("workspace_id"), c.Param("project_id"), c.Param("credential_id"), operatorSubject(c), ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCredentialResponse(cred, c.Param("project_id"), h.now().UTC()))
}

// Scopes handles GET /v1/project-scopes.
//
// @Summary     List the supported credential scopes
// @Description Advertises the scope vocabulary so a console does not hard-code
// @Description it and drift from the server. Adding a scope is a migration, so
// @Description this list changes rarely.
// @Description
// @Description Mounted OUTSIDE the workspace path on purpose: the vocabulary is
// @Description a property of the installation, not of a workspace, and putting
// @Description it under `/projects/scopes` would also collide with the
// @Description `/projects/{project_id}` route in the router's tree.
// @Description
// @Description **Operator only.**
// @Tags        projects
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} ScopesResponse
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, operator_only"
// @Router      /v1/project-scopes [get]
func (h *Handler) Scopes(c *gin.Context) {
	c.JSON(http.StatusOK, ScopesResponse{Scopes: authz.ScopeStrings(authz.AllScopes())})
}

// ─── Plumbing ───────────────────────────────────────────────────────────────

// operatorSubject is the acting operator's Keycloak subject, for created_by and
// revoked_by attribution.
//
// Every route in this handler is operator-only, so an identity is always
// present; the empty fallback exists for the same reason
// identityruntime.callerSubject has one, and never for a project — a project
// cannot reach these routes at all.
func operatorSubject(c *gin.Context) string {
	if id, ok := auth.IdentityFrom(c); ok && id != nil {
		return id.Subject
	}
	return ""
}

// bind decodes a JSON body. Uses encoding/json rather than gin's binder so
// decoding and validation stay separate: everything this can fail on is a
// malformed body, and field rules belong to the service.
func bind(c *gin.Context, dst any) bool {
	if err := json.NewDecoder(c.Request.Body).Decode(dst); err != nil {
		respondError(c, ErrInvalidRequest)
		return false
	}
	return true
}

// respondError writes the stable /v1 envelope.
//
// Anything that is not a catalogued *Error is an internal fault: the real error
// is logged with the request id and the client is told only internal_error.
// That is what keeps SQL fragments and constraint names off the wire.
func respondError(c *gin.Context, err error) {
	rid := requestid.FromContext(c)

	var domainErr *Error
	if !errors.As(err, &domainErr) {
		log.Error("unhandled project error (request_id=" + rid + "): " + err.Error())
		domainErr = ErrInternal
	} else if domainErr.Status >= http.StatusInternalServerError {
		log.Error("project " + domainErr.Code + " (request_id=" + rid + "): " + err.Error())
	}

	c.JSON(domainErr.Status, ErrorResponse{Error: ErrorBody{
		Code:      domainErr.Code,
		Message:   domainErr.Message,
		RequestID: rid,
	}})
}
