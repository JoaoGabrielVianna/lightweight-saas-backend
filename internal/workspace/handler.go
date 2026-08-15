package workspace

import (
	"encoding/json"
	"errors"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

var log = logger.New("workspace")

// Handler is the workspace HTTP surface. It owns no state beyond the service
// and performs no business logic — binding, status mapping, and nothing else.
//
// Authentication and authorization are NOT handled here. Every route is
// mounted inside the /v1 group, which carries the same chain as /admin/*:
// rate limit → RequireAuth → RequireRole("admin") → RequireLiveAdmin. A
// handler that also checked would be a second, divergeable copy of that rule.
type Handler struct {
	service *Service
}

// NewHandler constructs a Handler. service may be nil; the caller gates route
// registration on that rather than mounting handlers that would panic.
func NewHandler(service *Service) *Handler {
	// A nil service means the domain is not wired, and the composition root
	// reads a nil handler as "omit these routes". Returning a live handler over
	// a nil service would mount routes that nil-panic on first use — which was
	// unreachable before Slice 15 (only a nil repository produced a nil service,
	// and the composition root never passes one) and is reachable now that the
	// transaction runner and the audit writer are required collaborators.
	if service == nil {
		return nil
	}
	return &Handler{service: service}
}

// List handles GET /v1/workspaces.
//
// @Summary     List workspaces
// @Description Returns workspaces ordered by name, then id. By default only
// @Description active workspaces are returned — archived ones are operational
// @Description history rather than part of the working set. Requires the
// @Description caller's token to carry the realm `admin` role.
// @Tags        workspaces
// @Produce     json
// @Security    BearerAuth
// @Param       status query string false "which workspaces to return" Enums(active, archived, all) default(active)
// @Success     200 {object} ListWorkspacesResponse
// @Failure     400 {object} ErrorResponse "invalid_status_filter"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces [get]
func (h *Handler) List(c *gin.Context) {
	items, err := h.service.List(c.Request.Context(), c.Query("status"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toListResponse(items))
}

// Create handles POST /v1/workspaces.
//
// @Summary     Create a workspace
// @Description Creates an active workspace. `slug` is optional: when omitted
// @Description it is derived from `name`. When supplied it is trimmed and
// @Description lowercased but never rewritten — an unusable slug is reported
// @Description rather than silently changed. Slugs are globally unique and are
// @Description never released, including by archiving.
// @Tags        workspaces
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body CreateWorkspaceRequest true "workspace to create"
// @Success     201 {object} WorkspaceResponse
// @Failure     400 {object} ErrorResponse "workspace_name_required, workspace_slug_invalid, workspace_slug_reserved, invalid_request"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     409 {object} ErrorResponse "workspace_slug_taken"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces [post]
func (h *Handler) Create(c *gin.Context) {
	var req CreateWorkspaceRequest
	if err := decodeJSON(c, &req); err != nil {
		respondError(c, err)
		return
	}

	// The event is built BEFORE the call and completed by the service inside
	// the transaction, so the row that lands names the workspace that was
	// actually created rather than the one the request hoped for.
	//
	// On the failure path the workspace is still empty, which is correct and
	// deliberate: a creation that did not happen belongs to no workspace, so the
	// event reaches the log and the ring and is unreachable through
	// GET /v1/workspaces/{id}/audit. There is no workspace whose history it
	// could be part of.
	ev := logging.ControlPlaneEvent(c, audit.ActionWorkspaceCreated)
	w, err := h.service.Create(c.Request.Context(), CreateInput{Name: req.Name, Slug: req.Slug}, ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toResponse(w))
}

// Get handles GET /v1/workspaces/{workspace_id}.
//
// @Summary     Get a workspace
// @Description Returns one workspace by public id. Archived workspaces remain
// @Description individually readable. Accepts `ws_<uuid>`; a bare UUID is also
// @Description accepted as a development convenience.
// @Tags        workspaces
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} WorkspaceResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id} [get]
func (h *Handler) Get(c *gin.Context) {
	w, err := h.service.Get(c.Request.Context(), c.Param("workspace_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(w))
}

// Update handles PATCH /v1/workspaces/{workspace_id}.
//
// @Summary     Update a workspace
// @Description Renames a workspace. `name` is the only mutable field: `slug`
// @Description is immutable, and `status` changes go through the archive
// @Description operation. Sending either is rejected rather than ignored, so a
// @Description client never believes a change took effect when it did not.
// @Description Archived workspaces cannot be modified.
// @Tags        workspaces
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       body body UpdateWorkspaceRequest true "fields to change"
// @Success     200 {object} WorkspaceResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, workspace_name_required, invalid_request"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id} [patch]
func (h *Handler) Update(c *gin.Context) {
	var req UpdateWorkspaceRequest
	if err := decodeJSON(c, &req); err != nil {
		respondError(c, err)
		return
	}

	// Immutable fields are rejected before anything is written. Silently
	// dropping them would return 200 to a client that believes it renamed a
	// slug — a divergence that surfaces much later, somewhere else.
	if req.Slug != nil {
		respondError(c, immutableFieldError("slug"))
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

	// The path values seed the event so a FAILURE is still attributed to the
	// workspace the caller named. On success the service overwrites both with
	// the row it actually touched, which is the authoritative answer.
	ev := logging.ControlPlaneEvent(c, audit.ActionWorkspaceRenamed)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: "workspace", ID: c.Param("workspace_id")}

	w, err := h.service.UpdateName(c.Request.Context(), c.Param("workspace_id"), *req.Name, ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(w))
}

// Archive handles POST /v1/workspaces/{workspace_id}/archive.
//
// @Summary     Archive a workspace
// @Description Moves a workspace to the archived state and stamps
// @Description `archived_at`. Idempotent: archiving an already-archived
// @Description workspace succeeds and returns it unchanged, so a retried
// @Description request is safe. Nothing is deleted, the slug is not released,
// @Description and there is no reactivation path.
// @Tags        workspaces
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} WorkspaceResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/archive [post]
func (h *Handler) Archive(c *gin.Context) {
	ev := logging.ControlPlaneEvent(c, audit.ActionWorkspaceArchived)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: "workspace", ID: c.Param("workspace_id")}

	w, err := h.service.Archive(c.Request.Context(), c.Param("workspace_id"), ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(w))
}

// decodeJSON reads the request body into dst.
//
// It uses encoding/json directly rather than gin's ShouldBindJSON so that
// decoding and validation stay separate concerns: everything this can fail on
// is a malformed body (bad syntax, wrong type, absent body), which is exactly
// invalid_request. Field-level rules — required, blank-after-trim, length —
// belong to the service, which owns the specific stable codes for them.
//
// DisallowUnknownFields is deliberately NOT set: rejecting unrecognized keys
// would break any client that round-trips a response object, and the fields
// that must not be silently accepted (slug, status on PATCH) are declared and
// rejected explicitly instead.
func decodeJSON(c *gin.Context, dst any) error {
	if err := json.NewDecoder(c.Request.Body).Decode(dst); err != nil {
		return ErrInvalidRequest
	}
	return nil
}

// respondError writes the stable /v1 error envelope.
//
// Anything that is not a catalogued *Error is an internal fault: the real
// error is logged with the request id and the client is told only
// internal_error. That is what keeps database messages, constraint names and
// SQL fragments off the wire — the requirement is enforced here, once, rather
// than at each call site.
func respondError(c *gin.Context, err error) {
	rid := requestid.FromContext(c)

	var domainErr *Error
	if !errors.As(err, &domainErr) {
		log.Error("unhandled workspace error (request_id=" + rid + "): " + err.Error())
		domainErr = ErrInternal
	} else if domainErr.Status >= http.StatusInternalServerError {
		log.Error("workspace " + domainErr.Code + " (request_id=" + rid + "): " + err.Error())
	}

	c.JSON(domainErr.Status, ErrorResponse{Error: ErrorBody{
		Code:      domainErr.Code,
		Message:   domainErr.Message,
		RequestID: rid,
	}})
}
