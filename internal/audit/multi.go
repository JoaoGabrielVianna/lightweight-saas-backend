package audit

import "context"

// Multi fans an audit Event out to every recorder it contains, in order.
// Each downstream recorder receives the same Event value; if one needs
// to mutate fields it MUST copy first (Event has no embedded references
// today, so this is a non-issue, but the contract matters for Extra).
//
// Construction:
//
//	rec := audit.Multi{primarySink, memoryBuffer}
//	audit.SetDefault(rec)
//
// Nil entries are silently skipped so callers can pass conditional
// sinks (e.g. a feature-flagged debug sink) without conditionals at the
// call site.
type Multi []Recorder

// Record satisfies the Recorder interface. A panic in one recorder must
// not prevent later recorders from running — audit events are best-
// effort and one broken sink shouldn't blind the rest of the chain.
func (m Multi) Record(ctx context.Context, e Event) {
	for _, r := range m {
		if r == nil {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			r.Record(ctx, e)
		}()
	}
}
