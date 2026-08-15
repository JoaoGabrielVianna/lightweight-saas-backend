package auditlog

import (
	"net"
	"strings"
)

// Redaction: what may enter the table, decided by allowlist.
//
// This file exists because the two fields an emitter fills from RUNTIME VALUES
// — the failure reason and the extra map — are the two that can carry a secret,
// and neither is safe to store as given.
//
// The rule is the same for both and it is the only rule that holds up: **map
// onto a closed set, never sanitise an open one.** A denylist ("strip anything
// that looks like a password") fails the first time an upstream error phrases
// something a way nobody anticipated. An allowlist fails closed: an
// unrecognised value is dropped, and the cost is a missing detail rather than a
// leaked one.
//
// Anyone with `audit:read` can read every row this produces, including a
// project credential belonging to a backend the operator does not control.

// reasonCodeUnclassified is what a failure gets when its error does not map to
// a known code.
//
// It is a marker, not a message. The alternative — storing the original text —
// is exactly the leak: `audit.Event.Reason` is `err.Error()`, and the errors
// reaching it include provider responses and driver output.
//
// The real cause is not lost: it is in the log line for the same request_id,
// which is why request_id is persisted. The trail says WHAT failed and points
// at where WHY is written down.
const reasonCodeUnclassified = "unclassified_error"

// knownReasonCodes is the closed vocabulary a stored reason may take.
//
// Every entry is a code the /v1 surface already publishes, so an operator
// reading the trail sees the same word the API returned. They are matched as
// SUBSTRINGS of the error text, because domain errors render as their code
// (`(*Error).Error()` returns `e.Code`) but are often wrapped with context by
// the time they reach the emitter.
//
// Substring matching is safe here BECAUSE the output is the constant, never the
// input: a match copies the entry from this list into the row. An error text
// that happens to contain two codes yields the first in this order, which is
// deterministic and good enough for a machine-readable hint.
var knownReasonCodes = []string{
	// Authorization and capability.
	"workspace_mismatch",
	"insufficient_scope",
	"operator_only",
	"role_privileged",
	"forbidden",

	// Provider and connection state.
	"provider_forbidden",
	"provider_unavailable",
	"provider_credentials_unavailable",
	"connection_read_only",
	"workspace_connection_missing",
	"workspace_connection_unusable",

	// Resource state.
	"workspace_not_found",
	"workspace_archived",
	"user_not_found",
	"role_not_found",
	"session_not_found",
	"invitation_not_found",
	"project_not_found",
	"project_archived",
	"credential_not_found",
	"credential_already_revoked",
	"credential_limit_reached",
	"role_already_exists",
	"role_reserved",
	"project_name_taken",
	"workspace_slug_taken",

	// Request shape.
	"invalid_request",
	"invalid_scope",
	"invalid_workspace_id",
	"invalid_user_id",
	"invalid_role_name",
	"invalid_session_id",

	// Infrastructure.
	"internal_error",
}

// reasonCodeFor maps a failure's error text onto a stored code.
//
// Returns the marker for anything unrecognised. It never returns any part of
// the input.
func reasonCodeFor(reason string) string {
	if reason == "" {
		return ""
	}
	lowered := strings.ToLower(reason)
	for _, code := range knownReasonCodes {
		if strings.Contains(lowered, code) {
			return code
		}
	}
	return reasonCodeUnclassified
}

// metadataAllowlist is the per-event set of extra keys that may be stored.
//
// Per EVENT, not global. A key that is harmless on one event can be dangerous
// on another, and a global allowlist would have to be the union — which is the
// widest possible answer to a question that deserves the narrowest.
//
// Today there is one entry, because there is one emitter using Extra:
// `project.Handler.CreateCredential` records the scopes a new credential was
// granted. That is the single most useful fact for reconstructing what a leaked
// key could do, and it is not derivable from anything else in the row once the
// credential is revoked and its scopes are gone from the live table.
//
// Everything else is dropped. `RecordWorkspaceMutationExtra` is the only way to
// set Extra, it has one production call site, and adding a second should be a
// decision made here rather than a side effect made there.
var metadataAllowlist = map[string]map[string]bool{
	"project_credential.created": {"scopes": true},
}

// maxMetadataValues bounds how many keys survive, so a call site that starts
// passing a map derived from a request body cannot turn one event into a large
// row.
const maxMetadataValues = 8

// allowlistMetadata filters Extra down to what this event type may store.
//
// Returns nil rather than an empty map when nothing survives, so the column is
// NULL rather than `{}` — "no metadata" and "metadata that was emptied" read
// the same in JSON, and NULL at least does not suggest a filter ran.
func allowlistMetadata(eventType string, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	allowed := metadataAllowlist[eventType]
	if len(allowed) == 0 {
		return nil
	}

	out := make(map[string]any, len(allowed))
	for key, value := range extra {
		if !allowed[key] {
			continue
		}
		safe, ok := safeMetadataValue(value)
		if !ok {
			continue
		}
		out[key] = safe
		if len(out) >= maxMetadataValues {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// maxMetadataStringLength bounds a single stored string.
const maxMetadataStringLength = 256

// safeMetadataValue accepts only shapes that cannot hide a payload.
//
// Scalars and flat string slices. NOT nested maps: a nested map is how an
// entire request body gets in behind one allowlisted key, and nothing needs
// one. Rejecting rather than flattening keeps the failure visible as a missing
// value instead of an unexpectedly-shaped one.
func safeMetadataValue(v any) (any, bool) {
	switch value := v.(type) {
	case string:
		return truncate(value, maxMetadataStringLength), true
	case bool, int, int32, int64, float32, float64:
		return value, true
	case []string:
		if len(value) > maxMetadataValues {
			value = value[:maxMetadataValues]
		}
		out := make([]string, 0, len(value))
		for _, s := range value {
			out = append(out, truncate(s, maxMetadataStringLength))
		}
		return out, true
	default:
		return nil, false
	}
}

// validIP returns the address only if it parses as one.
//
// The observed client address is derived from forwarded headers when the
// deployment is behind a proxy, which makes it caller-influenced. Parsing it
// is the allowlist form of the same rule this file applies everywhere else:
// the column holds an address or NULL, never arbitrary text, so nothing a
// caller sends can become free text in another reader's view of the history.
func validIP(s string) string {
	if net.ParseIP(strings.TrimSpace(s)) == nil {
		return ""
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Cut on a rune boundary: a column holding half a UTF-8 sequence renders as
	// a replacement character everywhere it is displayed.
	cut := max
	for cut > 0 && !isUTF8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

// isUTF8Start reports whether b begins a UTF-8 sequence (i.e. is not a
// continuation byte, which are 0b10xxxxxx).
func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }
