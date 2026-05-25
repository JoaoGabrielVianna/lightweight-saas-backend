package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/gin-gonic/gin"
)

// AuditHandler serves a read-only window into the in-process audit ring
// buffer. The buffer is filled by audit.Multi at process start and never
// touched by handlers — this type only reads.
//
// Scope is intentionally tight: a single list endpoint, newest-first,
// process-local, capped by the buffer's capacity. The UI labels every
// limitation explicitly so operators don't mistake this for a durable
// audit trail; durable history is the log stream (AuditSink → stdout →
// log shipper).
type AuditHandler struct {
	mem *audit.MemoryRecorder
}

// NewAuditHandler wraps a MemoryRecorder for HTTP serving. May be nil-
// safe at the call site: if mem is nil, route registration should be
// skipped at composition time rather than serving 500s.
func NewAuditHandler(mem *audit.MemoryRecorder) *AuditHandler {
	return &AuditHandler{mem: mem}
}

// auditEventDTO is the JSON shape returned to the admin console. It
// mirrors audit.Event but uses RFC3339 strings for the timestamp so the
// front-end doesn't have to parse Go's default time format.
type auditEventDTO struct {
	Action    string         `json:"action"`
	Actor     audit.Actor    `json:"actor"`
	Target    audit.Target   `json:"target"`
	IP        string         `json:"ip,omitempty"`
	Timestamp string         `json:"ts"`
	Reason    string         `json:"reason,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// listEventsResponse is the response envelope. Capacity and Dropped are
// exposed so the UI can honestly tell operators that older events may
// have rolled off the ring buffer.
type listEventsResponse struct {
	Events   []auditEventDTO `json:"events"`
	Count    int             `json:"count"`
	Capacity int             `json:"capacity"`
	Dropped  uint64          `json:"dropped"`
}

// ListEvents handles GET /admin/audit-events.
//
// @Summary     List recent audit events (admin)
// @Description Returns a newest-first snapshot of the in-process audit
// @Description ring buffer. Process-local and volatile: events from other
// @Description replicas are not visible, and a restart drops the buffer.
// @Description The durable trail is the structured log stream.
// @Tags        audit
// @Produce     json
// @Security    BearerAuth
// @Param       limit query int false "max events to return (default: capacity; clamped to capacity)"
// @Success     200 {object} listEventsResponse
// @Failure     401 {object} map[string]string "missing/invalid token"
// @Failure     403 {object} map[string]string "token lacks admin role"
// @Router      /admin/audit-events [get]
func (h *AuditHandler) ListEvents(c *gin.Context) {
	limit := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	events, dropped := h.mem.Snapshot(limit, time.Time{})

	out := make([]auditEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, auditEventDTO{
			Action:    string(e.Action),
			Actor:     e.Actor,
			Target:    e.Target,
			IP:        e.IP,
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
			Reason:    e.Reason,
			Extra:     e.Extra,
		})
	}
	c.JSON(http.StatusOK, listEventsResponse{
		Events:   out,
		Count:    len(out),
		Capacity: h.mem.Capacity(),
		Dropped:  dropped,
	})
}
