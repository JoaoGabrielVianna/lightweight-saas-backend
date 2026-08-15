package connection

import (
	"encoding/json"
	"errors"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"net/http"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

var log = logger.New("connection")

// Handler is the connection HTTP surface. Binding and status mapping only —
// no business logic, and no authentication: every route is mounted inside the
// /v1 group, which carries the same chain as /admin/*.
type Handler struct {
	service *Service
	now     func() time.Time
}

// NewHandler constructs a Handler. service may be nil; the caller gates route
// registration on that rather than mounting handlers that would panic.
func NewHandler(service *Service) *Handler {
	// See workspace.NewHandler: a nil service must omit the routes rather than
	// mount handlers that nil-panic.
	if service == nil {
		return nil
	}
	return &Handler{service: service, now: time.Now}
}

// List handles GET /v1/workspaces/{workspace_id}/connections.
//
// @Summary     List a workspace's connections
// @Description Returns the workspace's connections ordered by name, then id.
// @Description Unlike the workspace listing, the default here is **all**
// @Description statuses: the operator workflow is draft → verify → activate →
// @Description the previous one retires, and hiding two thirds of that would
// @Description make the endpoint unusable. Client secrets are never returned.
// @Tags        connections
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       status query string false "which connections to return" Enums(draft, active, retired, all) default(all)
// @Success     200 {object} ListConnectionsResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_status_filter"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections [get]
func (h *Handler) List(c *gin.Context) {
	items, err := h.service.List(c.Request.Context(), c.Param("workspace_id"), c.Query("status"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toListResponse(items, h.now().UTC()))
}

// Create handles POST /v1/workspaces/{workspace_id}/connections.
//
// @Summary     Create a connection
// @Description Creates a connection in **draft** state. The client secret is
// @Description sealed with AES-256-GCM before it reaches the database and is
// @Description never returned by any endpoint. A draft does nothing until it
// @Description has been verified and activated.
// @Tags        connections
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id path string true "workspace id" example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       body body CreateConnectionRequest true "connection to create"
// @Success     201 {object} ConnectionResponse
// @Failure     400 {object} ErrorResponse "connection_name_required, connection_base_url_invalid, connection_realm_required, connection_client_id_required, connection_client_secret_required, connection_provider_unsupported, invalid_workspace_id, invalid_request"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found"
// @Failure     409 {object} ErrorResponse "workspace_archived"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections [post]
func (h *Handler) Create(c *gin.Context) {
	var req CreateConnectionRequest
	if err := decodeJSON(c, &req); err != nil {
		respondError(c, err)
		return
	}

	ev := connectionEvent(c, audit.ActionConnectionCreated)
	conn, err := h.service.Create(c.Request.Context(), c.Param("workspace_id"), CreateInput{
		Name:         req.Name,
		Provider:     req.Provider,
		BaseURL:      req.BaseURL,
		Realm:        req.Realm,
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
	}, ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toResponse(conn, h.now().UTC()))
}

// Get handles GET /v1/workspaces/{workspace_id}/connections/{connection_id}.
//
// @Summary     Get a connection
// @Description Returns one connection. The client secret is never included.
// @Tags        connections
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id  path string true "workspace id"  example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       connection_id path string true "connection id" example(conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} ConnectionResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_connection_id"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found, connection_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections/{connection_id} [get]
func (h *Handler) Get(c *gin.Context) {
	conn, err := h.service.Get(c.Request.Context(), c.Param("workspace_id"), c.Param("connection_id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(conn, h.now().UTC()))
}

// Update handles PATCH /v1/workspaces/{workspace_id}/connections/{connection_id}.
//
// @Summary     Update a draft connection
// @Description Edits a **draft** connection's configuration. Changing
// @Description `base_url`, `realm`, `client_id` or `client_secret` resets the
// @Description verification — the stored verdict referred to coordinates that
// @Description no longer apply, and keeping it would let the connection be
// @Description activated on the strength of a probe against a different
// @Description provider. `status` and `provider` are rejected rather than
// @Description ignored: state changes go through activate and retire.
// @Tags        connections
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id  path string true "workspace id"  example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       connection_id path string true "connection id" example(conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Param       body body UpdateConnectionRequest true "fields to change"
// @Success     200 {object} ConnectionResponse
// @Failure     400 {object} ErrorResponse "invalid_connection_id, connection_name_required, connection_base_url_invalid, invalid_request"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found, connection_not_found"
// @Failure     409 {object} ErrorResponse "connection_not_draft, connection_retired"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections/{connection_id} [patch]
func (h *Handler) Update(c *gin.Context) {
	var req UpdateConnectionRequest
	if err := decodeJSON(c, &req); err != nil {
		respondError(c, err)
		return
	}
	if req.Status != nil {
		respondError(c, immutableFieldError("status"))
		return
	}
	if req.Provider != nil {
		respondError(c, immutableFieldError("provider"))
		return
	}
	if req.Name == nil && req.BaseURL == nil && req.Realm == nil && req.ClientID == nil && req.ClientSecret == nil {
		respondError(c, ErrInvalidRequest)
		return
	}

	ev := connectionEvent(c, audit.ActionConnectionUpdated)
	conn, err := h.service.Update(c.Request.Context(), c.Param("workspace_id"), c.Param("connection_id"), UpdateInput{
		Name:         req.Name,
		BaseURL:      req.BaseURL,
		Realm:        req.Realm,
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
	}, ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(conn, h.now().UTC()))
}

// Delete handles DELETE /v1/workspaces/{workspace_id}/connections/{connection_id}.
//
// @Summary     Delete a connection
// @Description Permanently removes a **draft** or **retired** connection and
// @Description its sealed credential. An active connection cannot be deleted:
// @Description retire it first, so taking a workspace's provider out of
// @Description service is a visible, separate step.
// @Tags        connections
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id  path string true "workspace id"  example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       connection_id path string true "connection id" example(conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     204 "deleted"
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_connection_id"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found, connection_not_found"
// @Failure     409 {object} ErrorResponse "connection_active_cannot_delete"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections/{connection_id} [delete]
func (h *Handler) Delete(c *gin.Context) {
	ev := connectionEvent(c, audit.ActionConnectionDeleted)
	err := h.service.Delete(c.Request.Context(), c.Param("workspace_id"), c.Param("connection_id"), ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Verify handles POST .../connections/{connection_id}/verify.
//
// @Summary     Verify a connection
// @Description Probes the provider and records the verdict. The probe is
// @Description strictly read-only: it reaches the provider, confirms the realm
// @Description exists, authenticates the admin client, then reads the realm
// @Description settings and lists one user. It creates no test user and
// @Description modifies nothing.
// @Description
// @Description The first three checks decide `health`. The two admin reads
// @Description decide `access_mode`: a service account that authenticates but
// @Description cannot read is `healthy` + `limited` — correctly configured and
// @Description under-privileged, which is a different fix from a wrong URL.
// @Description
// @Description A successful verification authorizes activation for one hour.
// @Tags        connections
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id  path string true "workspace id"  example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       connection_id path string true "connection id" example(conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} VerifyResponse "the probe ran; check report.ok for the verdict"
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_connection_id"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found, connection_not_found"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections/{connection_id}/verify [post]
func (h *Handler) Verify(c *gin.Context) {
	ev := connectionEvent(c, audit.ActionConnectionVerified)
	conn, report, err := h.service.Verify(c.Request.Context(), c.Param("workspace_id"), c.Param("connection_id"), ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	// 200 even when the probe failed: the verification RAN, and its verdict is
	// in the body. A 4xx/5xx here would say this API malfunctioned, which is
	// not what "your provider refused our credentials" means.
	c.JSON(http.StatusOK, VerifyResponse{
		Connection: toResponse(conn, h.now().UTC()),
		Report:     report,
	})
}

// Activate handles POST .../connections/{connection_id}/activate.
//
// @Summary     Activate a connection
// @Description Promotes a verified draft to **active** and retires the
// @Description workspace's previous active connection in the same transaction.
// @Description At most one connection per workspace is active, enforced by a
// @Description partial unique index rather than by application code.
// @Description
// @Description Requires a verification that passed within the last hour.
// @Description Unlike archive, this is deliberately **not** idempotent: a
// @Description repeat call returns `connection_already_active`, because a
// @Description caller retrying may believe they are switching away from a
// @Description different connection.
// @Tags        connections
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id  path string true "workspace id"  example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       connection_id path string true "connection id" example(conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} ConnectionResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_connection_id"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found, connection_not_found"
// @Failure     409 {object} ErrorResponse "connection_not_verified, connection_verification_expired, connection_already_active, connection_retired, workspace_archived, workspace_has_active_connection"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections/{connection_id}/activate [post]
func (h *Handler) Activate(c *gin.Context) {
	ev := connectionEvent(c, audit.ActionConnectionActivated)
	conn, err := h.service.Activate(c.Request.Context(), c.Param("workspace_id"), c.Param("connection_id"), ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(conn, h.now().UTC()))
}

// Retire handles POST .../connections/{connection_id}/retire.
//
// @Summary     Retire a connection
// @Description Moves a connection to **retired**, its terminal state. Works on
// @Description a draft or an active connection; retiring an active one leaves
// @Description the workspace with no active connection. There is no
// @Description reactivation path — create a new connection instead. Retiring an
// @Description already-retired connection is a conflict, not a no-op.
// @Tags        connections
// @Produce     json
// @Security    BearerAuth
// @Param       workspace_id  path string true "workspace id"  example(ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d)
// @Param       connection_id path string true "connection id" example(conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301)
// @Success     200 {object} ConnectionResponse
// @Failure     400 {object} ErrorResponse "invalid_workspace_id, invalid_connection_id"
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Failure     404 {object} ErrorResponse "workspace_not_found, connection_not_found"
// @Failure     409 {object} ErrorResponse "connection_retired"
// @Failure     500 {object} ErrorResponse "internal_error"
// @Router      /v1/workspaces/{workspace_id}/connections/{connection_id}/retire [post]
func (h *Handler) Retire(c *gin.Context) {
	ev := connectionEvent(c, audit.ActionConnectionRetired)
	conn, err := h.service.Retire(c.Request.Context(), c.Param("workspace_id"), c.Param("connection_id"), ev)
	logging.RecordControlPlaneOutcome(c, ev, err)

	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(conn, h.now().UTC()))
}

// connectionEvent builds the event skeleton a transactional service completes.
//
// A helper rather than six inline blocks because every one of them needs the
// same three things right — the workspace from the path, the connection id from
// the path, and nothing else — and six copies is six chances to reach for a
// field that should not be there.
//
// What is deliberately absent from the target: the connection's NAME, its base
// URL, its realm and its client id. A connection audit event says which
// connection changed, not what it was pointed at. The client secret is not even
// reachable from here — the service never returns it — but the realm and URL
// are, and they describe an operator's internal infrastructure to anyone
// holding audit:read.
//
// The path id seeds the event so a FAILURE is still attributable: "who tried to
// activate conn_X and was refused" is exactly what an investigation looks for.
// On success the service overwrites the target with the row it actually
// touched, inside the transaction that commits both.
func connectionEvent(c *gin.Context, action audit.Action) *audit.Event {
	ev := logging.ControlPlaneEvent(c, action)
	ev.Workspace = c.Param("workspace_id")
	ev.Target = audit.Target{Kind: TargetKind, ID: c.Param("connection_id")}
	return ev
}

// decodeJSON reads the body with encoding/json rather than gin's binder, so
// decoding and validation stay separate: everything this can fail on is a
// malformed body, and field rules belong to the service.
func decodeJSON(c *gin.Context, dst any) error {
	if err := json.NewDecoder(c.Request.Body).Decode(dst); err != nil {
		return ErrInvalidRequest
	}
	return nil
}

// respondError writes the stable /v1 error envelope.
//
// Anything that is not a catalogued *Error is an internal fault: the real cause
// is logged with the request id and the client is told only internal_error.
// That is what keeps database messages, constraint names, provider URLs and —
// most importantly — anything derived from a credential off the wire.
func respondError(c *gin.Context, err error) {
	rid := requestid.FromContext(c)

	var domainErr *Error
	if !errors.As(err, &domainErr) {
		log.Error("unhandled connection error (request_id=" + rid + "): " + err.Error())
		domainErr = ErrInternal
	} else if domainErr.Status >= http.StatusInternalServerError {
		log.Error("connection " + domainErr.Code + " (request_id=" + rid + "): " + err.Error())
	}

	c.JSON(domainErr.Status, ErrorResponse{Error: ErrorBody{
		Code:      domainErr.Code,
		Message:   domainErr.Message,
		RequestID: rid,
	}})
}
