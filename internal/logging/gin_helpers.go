package logging

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
)

// ActorFromGin extracts the audit.Actor for the current request.
//
// It is the ONLY constructor of audit.Actor in production code, which is what
// makes the disjointness of the operator and project field sets a guarantee
// rather than a convention: there is no path here that writes a `prj_` id into
// Subject.
//
// Resolution order matters. The Principal is consulted FIRST, because it is the
// representation that can express both kinds of caller; the legacy
// IdentityFrom path is the fallback for surfaces that predate principals
// (/admin/*, /me), where the caller is an operator by construction.
//
// Returns a zero Actor when neither is present. Callers still Record, so the
// event surfaces as "actor unknown" rather than disappearing silently.
func ActorFromGin(c *gin.Context) audit.Actor {
	if p, ok := auth.PrincipalFrom(c); ok && p != nil {
		switch {
		case p.IsProject():
			return audit.Actor{
				Type:         audit.ActorProject,
				ProjectID:    p.Project.ProjectID,
				CredentialID: p.Project.CredentialID,
			}
		case p.IsOperator():
			return operatorActor(p.Operator)
		}
	}

	// Legacy path: /admin/* and /me run RequireAuth, which stores an Identity
	// and no Principal.
	id, ok := auth.IdentityFrom(c)
	if !ok || id == nil {
		return audit.Actor{}
	}
	return operatorActor(id)
}

func operatorActor(id *auth.Identity) audit.Actor {
	return audit.Actor{
		Type:     audit.ActorOperator,
		Subject:  id.Subject,
		Email:    id.Email,
		Username: id.Username,
	}
}

// maxUserAgentLength bounds what is copied into an event.
//
// The User-Agent header is attacker-controlled input on its way to a log line
// and, later, to a database column. 256 characters is longer than any real
// client string and short enough that a crafted header cannot be used to bloat
// storage or to push other fields out of a truncated log view.
const maxUserAgentLength = 256

// UserAgentFromGin returns the caller's declared client, bounded.
func UserAgentFromGin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	ua := c.Request.UserAgent()
	if len(ua) > maxUserAgentLength {
		return ua[:maxUserAgentLength]
	}
	return ua
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
	if base.RequestID == "" {
		// Empty on /admin/*, where requestid.Middleware is deliberately not
		// mounted. That absence is information, not a gap to paper over.
		base.RequestID = requestid.FromContext(c)
	}
	if base.UserAgent == "" {
		base.UserAgent = UserAgentFromGin(c)
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
	recordMutation(c, audit.Event{Action: action, Target: target}, err)
}

// RecordWorkspaceMutation is the workspace-scoped emitter. Identical to
// RecordMutation except that the event names the workspace whose provider the
// mutation was routed through.
//
// A separate function rather than an extra parameter on RecordMutation: the
// legacy /admin/* call sites have no workspace and never will, and threading an
// empty string through thirteen of them would invite someone to pass something
// plausible-looking into it.
func RecordWorkspaceMutation(c *gin.Context, workspacePublicID string, action audit.Action, target audit.Target, err error) {
	recordMutation(c, audit.Event{Action: action, Target: target, Workspace: workspacePublicID}, err)
}

// RecordWorkspaceMutationExtra is RecordWorkspaceMutation with per-event
// detail attached.
//
// Extra is the only free-text region of an event, so it is also the only place
// a caller could put something that should not be logged. Its one production
// use today is the scope list granted to a new credential — which is the fact
// an operator most needs when reconstructing what a leaked key could do, and
// which is not derivable from anything else in the record.
func RecordWorkspaceMutationExtra(c *gin.Context, workspacePublicID string, action audit.Action, target audit.Target, extra map[string]any, err error) {
	recordMutation(c, audit.Event{
		Action:    action,
		Target:    target,
		Workspace: workspacePublicID,
		Extra:     extra,
	}, err)
}

func recordMutation(c *gin.Context, base audit.Event, err error) {
	e := EventFromGin(c, base)
	if err != nil {
		e.Reason = err.Error()
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	audit.Record(ctx, e)
}

// ─── The control plane (Slice 15 / TD-033) ──────────────────────────────────
//
// Control-plane mutations do not use RecordWorkspaceMutation for their SUCCESS
// event, and the difference is the point of the slice.
//
// Their domain row and their audit row live in the same PostgreSQL, so the two
// can be made one transaction — and a service that can roll a mutation back
// when its audit row will not write is strictly better than a handler that
// records the mutation after it has already committed. The handler's job
// shrinks to two things: build the event skeleton before the call, and decide
// what to do with the outcome after it.
//
// Provider mutations keep the old path exactly. A Keycloak user cannot be
// rolled back by a PostgreSQL transaction, so failing the response there would
// invite a retry that creates a second user — see auditlog.Recorder.Record.

// ControlPlaneEvent builds the event skeleton a transactional service completes.
//
// It carries everything derivable from the REQUEST — who, from where, under
// which correlation id, doing what. What it deliberately does not carry is the
// workspace and the target, because for a create those do not exist yet and for
// everything else the authoritative value is the row the service actually
// touched. The service fills them in from its own result, inside the
// transaction, which is what makes the audit row describe what happened rather
// than what was asked for.
func ControlPlaneEvent(c *gin.Context, action audit.Action) *audit.Event {
	e := EventFromGin(c, audit.Event{Action: action})
	return &e
}

// RecordControlPlaneOutcome closes out a transactional mutation.
//
// Three outcomes, three different correct behaviours, and getting them wrong is
// how an audit trail starts lying:
//
//	success            the durable row is ALREADY written, inside the
//	                   committed transaction. Emit to the remaining sinks — the
//	                   log line and the in-process ring — with the marker set so
//	                   the durable recorder skips the row it just wrote.
//
//	domain failure     nothing committed, so there is nothing to be atomic
//	                   with. The failure event is best-effort and goes down the
//	                   ordinary path, exactly as before this slice.
//
//	audit failure      the mutation was rolled back BECAUSE the audit store
//	                   could not be written. Emitting a failure event here would
//	                   send a second write to the store that just failed —
//	                   turning a transient error into a retry storm against a
//	                   struggling database. It is logged and counted instead.
//	                   auditlog.Recorder.RecordTx has already done both; this
//	                   branch exists so no future edit reintroduces the write.
//
// The third branch is why this is a named function rather than an `if err !=
// nil` at each of fourteen call sites.
func RecordControlPlaneOutcome(c *gin.Context, ev *audit.Event, err error) {
	if ev == nil {
		return
	}

	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}

	switch {
	case err == nil:
		ev.PersistedInTransaction = true
		audit.Record(ctx, *ev)

	case errors.Is(err, audit.ErrNotRecorded):
		// Deliberately silent on the audit channel. See above.
		return

	default:
		ev.Reason = err.Error()
		audit.Record(ctx, *ev)
	}
}
