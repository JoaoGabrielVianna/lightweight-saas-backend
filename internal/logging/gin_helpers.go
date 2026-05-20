package logging

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

// ActorFromGin extracts the audit.Actor from the validated identity that
// auth middleware stashed on the gin context. Returns a zero Actor when
// no identity is present — callers should still call Record so the event
// surfaces as "actor unknown" rather than disappearing silently.
func ActorFromGin(c *gin.Context) audit.Actor {
	id, ok := auth.IdentityFrom(c)
	if !ok || id == nil {
		return audit.Actor{}
	}
	return audit.Actor{
		Subject:  id.Subject,
		Email:    id.Email,
		Username: id.Username,
	}
}

// IPFromGin returns the client IP that gin resolved via TrustedProxies
// settings. Pulled out as a helper so audit call sites read uniformly
// and tests can stub it in one place.
func IPFromGin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return c.ClientIP()
}

// EventFromGin assembles an audit.Event with who/ip already populated
// from the request context. Callers fill in Action/Target (and Reason
// or Extra if relevant) and hand it to audit.Record.
//
// Usage:
//
//	audit.Record(ctx, logging.EventFromGin(c, audit.Event{
//	    Action: audit.ActionUserDeleted,
//	    Target: audit.Target{Kind: "user", ID: targetSub, Name: targetEmail},
//	}))
func EventFromGin(c *gin.Context, base audit.Event) audit.Event {
	if base.Actor == (audit.Actor{}) {
		base.Actor = ActorFromGin(c)
	}
	if base.IP == "" {
		base.IP = IPFromGin(c)
	}
	return base
}

// RecordMutation is the one-line emitter every mutation handler calls.
// It builds an audit.Event from the request context, attaches err.Error()
// as Reason when err is non-nil, and dispatches via audit.Record.
//
// Either path (success OR failure) emits exactly one event — the mission
// invariant is "every mutation MUST emit who/action/target/timestamp/ip;
// failures MUST also emit reason." Centralising the branch here keeps
// the 13 handler call sites readable and impossible to skew apart.
//
// Usage in a handler:
//
//	err := h.service.DeleteUser(c.Request.Context(), callerSubject(c), targetID)
//	logging.RecordMutation(c, audit.ActionUserDeleted,
//	    audit.Target{Kind: "user", ID: targetID}, err)
//	if err != nil {
//	    handleError(c, err)
//	    return
//	}
//	c.Status(http.StatusNoContent)
func RecordMutation(c *gin.Context, action audit.Action, target audit.Target, err error) {
	e := EventFromGin(c, audit.Event{
		Action: action,
		Target: target,
	})
	if err != nil {
		e.Reason = err.Error()
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	audit.Record(ctx, e)
}
