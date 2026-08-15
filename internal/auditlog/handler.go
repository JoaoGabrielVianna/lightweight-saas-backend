package auditlog

import (
	"errors"
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

// Handler is the workspace audit read surface.
type Handler struct {
	service *Service
}

// NewHandler constructs the handler. Returns nil for a nil service so the
// composition root omits the route rather than mounting one that 500s.
func NewHandler(service *Service) *Handler {
	if service == nil {
		return nil
	}
	return &Handler{service: service}
}

// publicEventID renders a stored UUID as evt_<uuid>.
func publicEventID(uuid string) string {
	if uuid == "" {
		return ""
	}
	return publicid.Format(EventPrefix, uuid)
}

// List handles GET /v1/workspaces/{workspace_id}/audit.
//
// @Summary     List a workspace's audit history
// @Description Returns the durable audit trail for this workspace, newest first.
// @Description
// @Description **Required scope (project credentials):** `audit:read`.
// @Description Operators are authorized by the realm `admin` role instead.
// @Description
// @Description The trail records ATTEMPTS TO CHANGE STATE by an identified actor —
// @Description who did what, to which resource, whether it worked, and on which
// @Description request. It is not a request log: reads are absent, and so is
// @Description traffic that never got past authentication.
// @Description
// @Description **Pagination is cursor-based.** Pass the `next_cursor` from the
// @Description previous page; its ABSENCE means there is no more history. Offset
// @Description pagination is not offered: this table only ever grows at the head,
// @Description so an offset shifts under a client between pages.
// @Description
// @Description **This endpoint never returns secret material.** No password, no
// @Description credential secret or hash, no connection secret, no provider token,
// @Description and no request body — regardless of who is asking.
// @Tags        audit
// @Produce     json
// @Security    BearerAuth
// @Security    ProjectKeyAuth
// @Param       workspace_id path  string false "workspace id" example(ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       event        query string false "exact event type, e.g. user.created"
// @Param       actor_type   query string false "operator or project" Enums(operator, project)
// @Param       outcome      query string false "success or failure"  Enums(success, failure)
// @Param       from         query string false "inclusive lower bound, RFC 3339" example(2026-08-01T00:00:00Z)
// @Param       to           query string false "inclusive upper bound, RFC 3339" example(2026-08-31T23:59:59Z)
// @Param       cursor       query string false "opaque cursor from the previous page"
// @Param       limit        query int    false "page size, 1-200 (default 50)"
// @Success     200 {object} ListEventsResponse
// @Failure     400 {object} ErrorResponse "invalid_request, invalid_workspace_id"
// @Failure     401 {object} ErrorResponse "credential_invalid"
// @Failure     403 {object} ErrorResponse "forbidden, workspace_mismatch, insufficient_scope"
// @Failure     503 {object} ErrorResponse "audit_unavailable"
// @Router      /v1/workspaces/{workspace_id}/audit [get]
func (h *Handler) List(c *gin.Context) {
	// The workspace comes from the PATH, which authorization has already
	// checked: a project credential reaching here has been confirmed bound to
	// this workspace, and an operator has been confirmed a live realm admin.
	//
	// There is deliberately no `workspace_id` query parameter anywhere in this
	// handler. The boundary is the path, and offering a second way to name a
	// workspace would create a second thing to authorize.
	workspaceUUID, err := publicid.Parse(publicid.WorkspacePrefix, c.Param("workspace_id"))
	if err != nil {
		respondError(c, ErrInvalidWorkspaceID)
		return
	}

	params := ListParams{
		EventType: c.Query("event"),
		ActorType: c.Query("actor_type"),
		Outcome:   c.Query("outcome"),
		From:      c.Query("from"),
		To:        c.Query("to"),
		Cursor:    c.Query("cursor"),
		Limit:     c.Query("limit"),
	}

	page, err := h.service.List(c.Request.Context(), workspaceUUID, params)
	if err != nil {
		respondError(c, err)
		return
	}

	limit := page.appliedLimit(params.Limit)
	c.JSON(http.StatusOK, newListEventsResponse(page, limit))

	// Reading the audit trail is deliberately NOT audited.
	//
	// Recording reads would make the trail grow from being looked at, and the
	// console polls it. More importantly it would mean the answer to "what
	// happened here" is mostly "someone asked what happened here", which buries
	// the mutations the trail exists for. Access to the endpoint is in the
	// request log, with the same request id.
}

// appliedLimit reports the page size that was actually used, for the response.
//
// Derived rather than re-parsed: the service already validated and defaulted
// the value, and parsing the raw string a second time here is how the two
// answers drift.
func (p Page) appliedLimit(raw string) int {
	if raw == "" {
		return defaultPageSize
	}
	n, err := parseLimit(raw)
	if err != nil {
		return defaultPageSize
	}
	return n
}

// ErrInvalidWorkspaceID mirrors the code every other /v1 surface returns for a
// malformed workspace id, so a client has one branch rather than one per
// package.
var ErrInvalidWorkspaceID = &Error{
	Code:    "invalid_workspace_id",
	Message: "Workspace id must be in the form ws_<uuid>",
	Status:  http.StatusBadRequest,
}

// respondError writes the /v1 envelope.
//
// Anything that is not a catalogued *Error is infrastructure failing: the real
// error goes to the log with the request id, and the client is told
// `audit_unavailable`. That is what keeps a driver message — which can name a
// host, a user, or a constraint — off a surface that a project credential can
// read.
func respondError(c *gin.Context, err error) {
	rid := requestid.FromContext(c)

	var domainErr *Error
	if !errors.As(err, &domainErr) {
		log.Error("audit query failed (request_id=" + rid + "): " + err.Error())
		domainErr = ErrAuditUnavailable
	}

	c.JSON(domainErr.Status, ErrorResponse{Error: ErrorBody{
		Code:      domainErr.Code,
		Message:   domainErr.Message,
		Field:     domainErr.Field,
		RequestID: rid,
	}})
}
