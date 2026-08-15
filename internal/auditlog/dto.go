package auditlog

import "time"

// The wire types.
//
// Deliberately NOT the Record. A durable row and a response are two contracts
// with two lifetimes, and the difference here is not cosmetic — three fields
// exist in the table and never in the response:
//
//	source_ip    an operator's home address, readable by any credential
//	             holding audit:read. The trail answers WHO, and the actor's
//	             identity already does that; their network location is a
//	             separate disclosure nobody asked for.
//	user_agent   same reasoning, less severe. Kept in the table because it is
//	             the cheapest way to tell two deployments sharing a credential
//	             apart during an incident, which is an operator-with-database
//	             activity, not an API one.
//	workspace_id redundant: it is in the path the caller just used, and echoing
//	             it invites a client to read the response's copy instead of the
//	             one it is authorized for.
//
// Adding a field here is a decision about disclosure. Making it a separate type
// is what forces that decision to be made once, visibly, rather than by whoever
// adds a column next.

// EventResponse is one audit event.
type EventResponse struct {
	ID    string `json:"id"      example:"evt_3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
	Event string `json:"event"   example:"project_credential.revoked"`

	// Outcome is "success" or "failure".
	Outcome string `json:"outcome" example:"success" enums:"success,failure"`

	Actor    ActorResponse     `json:"actor"`
	Resource *ResourceResponse `json:"resource,omitempty"`

	// ReasonCode is present only on a failure, and is drawn from the closed /v1
	// error vocabulary. It is never an upstream error message: the real cause
	// is in the log line for this request_id.
	ReasonCode string `json:"reason_code,omitempty" example:"provider_unavailable"`

	// Metadata is per-event detail, allowlisted per event type at write time.
	Metadata map[string]any `json:"metadata,omitempty"`

	RequestID  string    `json:"request_id,omitempty" example:"3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
	OccurredAt time.Time `json:"occurred_at"          example:"2026-08-10T14:03:11Z"`
}

// ActorResponse is who acted, disjoint by type exactly as the stored row is.
//
// An operator row carries subject and email; a project row carries the project
// and credential ids and NEVER a subject. `omitempty` throughout, so the shape
// itself tells a reader which kind of principal this was.
type ActorResponse struct {
	Type string `json:"type" example:"operator" enums:"operator,project"`

	Subject string `json:"subject,omitempty" example:"9c1e6679-7425-40de-944b-e07fc1f90ae7"`
	Email   string `json:"email,omitempty"   example:"ada@example.com"`

	ProjectID    string `json:"project_id,omitempty"    example:"prj_7c9e6679-7425-40de-944b-e07fc1f90ae7"`
	CredentialID string `json:"credential_id,omitempty" example:"key_9b2f4c1a-1111-4222-8333-444455556666"`
}

// ResourceResponse is what was acted upon.
type ResourceResponse struct {
	Type string `json:"type" example:"project_credential"`
	ID   string `json:"id"   example:"key_9b2f4c1a-1111-4222-8333-444455556666"`
}

// ListEventsResponse is one page.
//
// `items` rather than `events`, and a `pagination` object rather than a bare
// count: the other /v1 collections return `{<plural>, count}` because they are
// unpaginated and complete. This one is neither, and reusing that shape would
// let a client read `count` as "how many exist" when it is "how many are on
// this page" — which is the difference between paginating and stopping.
type ListEventsResponse struct {
	Items      []EventResponse `json:"items"`
	Pagination PaginationInfo  `json:"pagination"`
}

// PaginationInfo carries the position to resume from.
type PaginationInfo struct {
	// Count is the number of items IN THIS PAGE, never a total. A total would
	// mean a second COUNT(*) over an append-heavy table on every request, to
	// answer a question no client here needs.
	Count int `json:"count" example:"50"`

	// Limit is the page size actually applied.
	Limit int `json:"limit" example:"50"`

	// NextCursor is absent on the last page.
	//
	// Its ABSENCE is the end-of-history signal, not an empty page: a client
	// loops while this is present, so a correct client makes exactly as many
	// requests as there are pages.
	NextCursor string `json:"next_cursor,omitempty" example:"MTc1NDg0MTM5MTAwMDAwMDAwMC4z..."`
}

// newEventResponse projects a stored row onto the wire.
func newEventResponse(r Record) EventResponse {
	out := EventResponse{
		ID:         publicEventID(r.ID),
		Event:      r.EventType,
		Outcome:    string(r.Outcome),
		ReasonCode: r.ReasonCode,
		Metadata:   r.Metadata,
		RequestID:  r.RequestID,
		OccurredAt: r.OccurredAt.UTC(),
		Actor: ActorResponse{
			Type:         string(r.ActorType),
			Subject:      r.ActorSubject,
			Email:        r.ActorEmail,
			ProjectID:    r.ActorProjectID,
			CredentialID: r.ActorCredentialID,
		},
	}
	if r.ResourceType != "" || r.ResourceID != "" {
		out.Resource = &ResourceResponse{Type: string(r.ResourceType), ID: r.ResourceID}
	}
	return out
}

func newListEventsResponse(page Page, limit int) ListEventsResponse {
	items := make([]EventResponse, 0, len(page.Items))
	for _, rec := range page.Items {
		items = append(items, newEventResponse(rec))
	}

	out := ListEventsResponse{
		Items:      items,
		Pagination: PaginationInfo{Count: len(items), Limit: limit},
	}
	if page.NextCursor != nil {
		out.Pagination.NextCursor = encodeCursor(*page.NextCursor)
	}
	return out
}

// ErrorResponse is the stable /v1 envelope.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody matches the envelope every other /v1 surface publishes, including
// the `field` key added in Slice 9.
type ErrorBody struct {
	Code      string `json:"code"                example:"invalid_request"`
	Message   string `json:"message"             example:"Request is invalid"`
	Field     string `json:"field,omitempty"     example:"cursor"`
	RequestID string `json:"request_id"          example:"3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
}
