package workspace

import "time"

// WorkspaceResponse is the wire representation of a workspace.
//
// It is a separate type from the domain model on purpose: the model may grow
// fields the API must not expose (an encrypted connection secret, an internal
// counter), and a shared struct would leak them the moment someone adds one.
// Every field here is deliberate.
type WorkspaceResponse struct {
	// ID is always the prefixed form, `ws_<uuid>`. The bare UUID is never
	// exposed.
	ID     string `json:"id"     example:"ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
	Slug   string `json:"slug"   example:"production"`
	Name   string `json:"name"   example:"Production"`
	Status string `json:"status" example:"active" enums:"active,archived"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// ArchivedAt is null for active workspaces and set for archived ones. The
	// database enforces that this and Status can never disagree.
	ArchivedAt *time.Time `json:"archived_at"`
}

// ListWorkspacesResponse wraps the collection in an object rather than
// returning a bare JSON array, so pagination fields can be added later without
// breaking every client.
type ListWorkspacesResponse struct {
	Workspaces []WorkspaceResponse `json:"workspaces"`
	Count      int                 `json:"count" example:"2"`
}

// CreateWorkspaceRequest is the POST /v1/workspaces body.
//
// The `validate:"required"` tag on Name is documentation for the OpenAPI
// generator, not a runtime check: gin's binder reads the `binding` tag, which
// is deliberately absent. Validation happens once, in the service, because two
// validators inevitably disagree — the binder would accept `{"name":"   "}`
// that the service then rejects, producing two different error codes for the
// same mistake.
type CreateWorkspaceRequest struct {
	// Name is required and must be non-blank after trimming.
	Name string `json:"name" validate:"required" example:"Production"`

	// Slug is optional. When omitted it is derived from Name; when present it
	// is only trimmed and lowercased, never slugified — an unusable slug is
	// reported rather than silently rewritten.
	Slug string `json:"slug,omitempty" example:"production"`
}

// UpdateWorkspaceRequest is the PATCH /v1/workspaces/{id} body.
//
// Name is a pointer so "field absent" is distinguishable from "field set to
// empty string": the first is a no-op, the second is a validation error.
//
// Slug and Status are declared even though neither can be changed. Declaring
// them is what lets the handler answer "slug is immutable" instead of silently
// ignoring the field — a client that believes it renamed a slug and got a 200
// would only discover otherwise much later.
type UpdateWorkspaceRequest struct {
	Name   *string `json:"name,omitempty" example:"Production EU"`
	Slug   *string `json:"slug,omitempty"`
	Status *string `json:"status,omitempty"`
}

// ErrorResponse is the stable /v1 error envelope.
//
//	{"error": {"code": "...", "message": "...", "request_id": "..."}}
//
// Nesting under `error` (rather than a flat {"code","message"}) keeps the
// success and failure shapes unambiguous for a client that checks for the key
// before looking at the status.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the stable code, human-readable prose, and the request id
// that ties the response to the server-side log line holding the real cause.
type ErrorBody struct {
	Code      string `json:"code"       example:"workspace_not_found"`
	Message   string `json:"message"    example:"Workspace not found"`
	RequestID string `json:"request_id" example:"3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
}

// toResponse converts a domain workspace to its wire form.
func toResponse(w *Workspace) WorkspaceResponse {
	return WorkspaceResponse{
		ID:         w.PublicID(),
		Slug:       w.Slug,
		Name:       w.Name,
		Status:     string(w.Status),
		CreatedAt:  w.CreatedAt,
		UpdatedAt:  w.UpdatedAt,
		ArchivedAt: w.ArchivedAt,
	}
}

// toListResponse converts a slice of domain workspaces. The Workspaces field
// is always a non-nil slice so an empty result marshals as `[]`, not `null` —
// clients iterate without a nil check.
func toListResponse(items []Workspace) ListWorkspacesResponse {
	out := make([]WorkspaceResponse, 0, len(items))
	for i := range items {
		out = append(out, toResponse(&items[i]))
	}
	return ListWorkspacesResponse{Workspaces: out, Count: len(out)}
}
