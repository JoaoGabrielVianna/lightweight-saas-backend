package project

import "time"

// ProjectResponse is the wire representation of a project.
//
// A separate type from the domain model on purpose: the model may grow fields
// the API must not expose, and a shared struct would leak them the moment
// someone adds one.
type ProjectResponse struct {
	ID          string `json:"id"           example:"prj_3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
	WorkspaceID string `json:"workspace_id" example:"ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d"`
	Name        string `json:"name"         example:"Billing worker"`
	Status      string `json:"status"       example:"active" enums:"active,archived"`

	// ActiveCredentials is how many credentials can currently authenticate.
	// Included in the listing so an operator can see at a glance which projects
	// hold live keys without opening each one.
	ActiveCredentials int `json:"active_credentials" example:"2"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at"`
}

// ListProjectsResponse wraps the collection so pagination can be added later
// without breaking clients.
type ListProjectsResponse struct {
	Projects []ProjectResponse `json:"projects"`
	Count    int               `json:"count" example:"2"`
}

// CredentialResponse is the wire representation of a credential.
//
// THE SECRET IS NOT HERE, AND CANNOT BE. The domain Credential type has no
// field for the plaintext, so no change to this conversion can expose it — the
// guarantee is structural, not a matter of remembering. `KeyPrefix` is the
// public lookup segment and is safe to show: it identifies the credential in a
// server log without being usable on its own.
//
// The hash is also absent. It discloses nothing, but printing a digest of a
// credential invites someone to treat it as an identifier and compare it.
type CredentialResponse struct {
	ID        string `json:"id"         example:"key_7c9e6679-7425-40de-944b-e07fc1f90ae7"`
	ProjectID string `json:"project_id" example:"prj_3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
	Label     string `json:"label"      example:"billing worker (staging)"`

	// KeyPrefix is the lookup segment of the token, e.g. "k3mzr7q2xwab5f9d".
	// Shown so an operator can match a key in a log line to a row here.
	KeyPrefix string `json:"key_prefix" example:"k3mzr7q2xwab5f9d"`

	Scopes []string `json:"scopes" example:"users:read,users:write"`

	// Status is the credential's effective state, computed at response time:
	// active, expired or revoked. Computed rather than stored because expiry is
	// a fact about now, not about the row.
	Status string `json:"status" example:"active" enums:"active,expired,revoked"`

	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`

	CreatedBy string  `json:"created_by,omitempty"`
	RevokedBy *string `json:"revoked_by,omitempty"`
}

// ListCredentialsResponse wraps the collection.
type ListCredentialsResponse struct {
	Credentials []CredentialResponse `json:"credentials"`
	Count       int                  `json:"count" example:"2"`
}

// CreateCredentialResponse is the ONLY response in this API that carries a
// credential secret, and it carries it exactly once.
//
// There is deliberately no endpoint that can return `secret` again: it is not
// stored, so no such endpoint could exist even if one were added. A client that
// loses it must create a new credential and revoke this one.
type CreateCredentialResponse struct {
	Credential CredentialResponse `json:"credential"`

	// Secret is the full token, e.g. lw_sk_<lookup>_<secret>. Shown once.
	Secret string `json:"secret" example:"lw_sk_REDACTED_REDACTED"`
}

// CreateProjectRequest is the POST body.
//
// `validate:"required"` is documentation for the OpenAPI generator, not a
// runtime check — the `binding` tag is deliberately absent so validation
// happens once, in the service, with a specific stable code per field.
type CreateProjectRequest struct {
	Name string `json:"name" validate:"required" example:"Billing worker"`
}

// UpdateProjectRequest is the PATCH body.
//
// Name is a pointer so "absent" is distinguishable from "set to empty". Status
// and WorkspaceID are declared even though neither can change: declaring them
// is what lets the handler answer "this is immutable" instead of silently
// ignoring the field. A client that believes it moved a project between
// workspaces and got a 200 would discover otherwise much later, and the whole
// authorization model rests on that binding never moving.
type UpdateProjectRequest struct {
	Name        *string `json:"name,omitempty" example:"Billing worker EU"`
	Status      *string `json:"status,omitempty"`
	WorkspaceID *string `json:"workspace_id,omitempty"`
}

// CreateCredentialRequest is the POST body for a credential.
type CreateCredentialRequest struct {
	// Label is required. An unlabelled key is one nobody dares revoke.
	Label string `json:"label" validate:"required" example:"billing worker (staging)"`

	// Scopes must be non-empty and drawn from the supported vocabulary. There
	// is no default and nothing is implied: a credential's power is always an
	// explicit choice.
	Scopes []string `json:"scopes" validate:"required" example:"users:read"`

	// ExpiresAt is optional. When set it must be in the future.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ScopesResponse advertises the supported scope vocabulary so a console does
// not hard-code it and drift from the server.
type ScopesResponse struct {
	Scopes []string `json:"scopes"`
}

// credentialStatus computes the effective state.
func credentialStatus(c *Credential, now time.Time) string {
	switch {
	case c.IsRevoked():
		return "revoked"
	case c.IsExpired(now):
		return "expired"
	default:
		return "active"
	}
}

func toProjectResponse(p *Project, activeCredentials int) ProjectResponse {
	return ProjectResponse{
		ID:                p.PublicID(),
		WorkspaceID:       p.WorkspacePublicID(),
		Name:              p.Name,
		Status:            string(p.Status),
		ActiveCredentials: activeCredentials,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
		ArchivedAt:        p.ArchivedAt,
	}
}

// toProjectListResponse converts a slice. Projects is always a non-nil slice so
// an empty result marshals as `[]`, not `null` — clients iterate without a nil
// check.
func toProjectListResponse(items []Project, counts map[string]int) ListProjectsResponse {
	out := make([]ProjectResponse, 0, len(items))
	for i := range items {
		out = append(out, toProjectResponse(&items[i], counts[items[i].ID]))
	}
	return ListProjectsResponse{Projects: out, Count: len(out)}
}

func toCredentialResponse(c *Credential, projectPublicID string, now time.Time) CredentialResponse {
	return CredentialResponse{
		ID:         c.PublicID(),
		ProjectID:  projectPublicID,
		Label:      c.Label,
		KeyPrefix:  c.KeyPrefix,
		Scopes:     c.Scopes,
		Status:     credentialStatus(c, now),
		ExpiresAt:  c.ExpiresAt,
		RevokedAt:  c.RevokedAt,
		LastUsedAt: c.LastUsedAt,
		CreatedAt:  c.CreatedAt,
		CreatedBy:  c.CreatedBy,
		RevokedBy:  c.RevokedBy,
	}
}

func toCredentialListResponse(items []Credential, projectPublicID string, now time.Time) ListCredentialsResponse {
	out := make([]CredentialResponse, 0, len(items))
	for i := range items {
		out = append(out, toCredentialResponse(&items[i], projectPublicID, now))
	}
	return ListCredentialsResponse{Credentials: out, Count: len(out)}
}
