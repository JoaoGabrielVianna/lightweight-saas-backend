// Package logging contains observability sinks that consume domain
// events (currently: audit.Event) and turn them into structured log
// lines via the project's existing logger. This package is the bridge
// between the provider-agnostic audit model and the actual log stream;
// nothing here is on the request hot path beyond a single fmt write.
package logging

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
)

// auditLogPrefix is the leading token on every audit log line. Downstream
// observability systems (grafana/loki, journalctl filters, the upcoming
// audit_log table loader in Sprint 4) are expected to grep on this
// prefix to separate audit lines from ordinary application chatter.
const auditLogPrefix = "audit "

// AuditSink is an audit.Recorder that emits one structured JSON line
// per event via the project logger. The output shape is:
//
//	audit {"action":"user.updated","actor":{...},"target":{...},"ip":"10.0.0.1","ts":"2026-05-20T12:00:00Z"}
//
// JSON is preferred over logfmt here because Actor/Target are nested
// records — flattening them would mean choosing arbitrary separators
// that downstream consumers would have to undo.
type AuditSink struct {
	log *logger.Logger
}

// NewAuditSink builds an AuditSink that writes to a dedicated "audit"
// logger origin. The origin is fixed so operators can filter on it
// without having to know which package emitted the event.
func NewAuditSink() *AuditSink {
	return &AuditSink{log: logger.New("audit")}
}

// Record implements audit.Recorder. Never errors — a malformed Event
// (which json.Marshal will never reject for our fixed shape) would still
// produce a valid line because every field has a safe zero value.
func (s *AuditSink) Record(_ context.Context, e audit.Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	// Force UTC so log lines from different timezones collate correctly.
	e.Timestamp = e.Timestamp.UTC()

	buf, err := json.Marshal(e)
	if err != nil {
		// json.Marshal cannot fail for our fixed schema, but the fallback
		// keeps the audit trail intact in the impossible case it did.
		s.log.Error(auditLogPrefix + `{"error":"audit-marshal-failed","action":"` + string(e.Action) + `"}`)
		return
	}
	s.log.Info(auditLogPrefix + string(buf))
}

// WireDefault installs a new AuditSink as the package-level recorder in
// internal/audit and returns the previous recorder so callers can chain
// or restore. Designed to be invoked once from bootstrap.
func WireDefault() audit.Recorder {
	return audit.SetDefault(NewAuditSink())
}

// WireDefaultWithMemory installs an audit.Multi recorder that fans every event
// out to the sinks that are wired.
//
//  1. AuditSink — one structured JSON line per event. Durable only if
//     something is shipping logs, which is why it is no longer the whole story.
//  2. durable — the PostgreSQL trail, when a store is available. This is the
//     authority for workspace history: it survives a restart, which is the one
//     thing the other two do not.
//  3. MemoryRecorder — a bounded ring the legacy /admin/audit-events endpoint
//     reads. Process-level and volatile, and kept exactly as it was: that
//     surface has no workspace and must stay byte-compatible.
//
// Three sinks, but not three authorities. Each answers a different question —
// "what is in the log stream", "what has ever happened in this workspace",
// "what just happened on this box" — and no consumer reads two of them for the
// same answer.
//
// durable may be nil, which is the deployment with no database. audit.Multi
// skips nil entries, so the fan-out needs no conditional here.
//
// Returned MemoryRecorder is the handle the legacy HTTP layer needs. Capacity
// is clamped by the recorder itself; a non-positive value becomes 1.
func WireDefaultWithMemory(capacity int, durable audit.Recorder) *audit.MemoryRecorder {
	mem := audit.NewMemoryRecorder(capacity)
	audit.SetDefault(audit.Multi{NewAuditSink(), durable, mem})
	return mem
}
