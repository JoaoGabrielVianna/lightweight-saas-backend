package connection

import "time"

// ConnectionResponse is the wire representation of a connection.
//
// THE SECRET IS NOT HERE, AND CANNOT BE. The domain Connection type has no
// field for it, so no change to this conversion can accidentally expose it —
// the guarantee is structural, not a matter of remembering. `HasClientSecret`
// is the only signal that one exists.
type ConnectionResponse struct {
	ID          string `json:"id"           example:"conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
	WorkspaceID string `json:"workspace_id" example:"ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d"`
	Name        string `json:"name"         example:"Production Keycloak"`
	Provider    string `json:"provider"     example:"keycloak" enums:"keycloak"`
	Status      string `json:"status"       example:"draft" enums:"draft,active,retired"`

	BaseURL  string `json:"base_url"  example:"https://kc.example.com"`
	Realm    string `json:"realm"     example:"saas"`
	ClientID string `json:"client_id" example:"saas-backend-admin"`

	// HasClientSecret reports that a credential is stored. It is always true
	// today because the column is NOT NULL; it exists as a field so that
	// clients render "secret configured" from the API rather than assuming,
	// and so the shape already fits if a secret-less auth method (mTLS,
	// private_key_jwt) is ever added.
	HasClientSecret bool `json:"has_client_secret" example:"true"`

	Health        string `json:"health"                     example:"healthy" enums:"unknown,healthy,unhealthy"`
	HealthMessage string `json:"health_message,omitempty"`

	// AccessMode is what the service account was proven able to do.
	// `full` is claimed ONLY when write capability was positively proven —
	// see connection.AccessMode. A client may enable mutation controls on
	// `full` and must not on `read_only` or `limited`.
	AccessMode string `json:"access_mode" example:"full" enums:"unknown,full,read_only,limited"`

	// CanWrite is AccessMode's write verdict, precomputed so a client does not
	// re-implement the rule (and get `unknown` wrong in either direction).
	// It is the field a UI should gate mutation controls on.
	CanWrite bool `json:"can_write" example:"true"`

	LastVerifiedAt *time.Time `json:"last_verified_at"`

	// Verified is IsVerified evaluated at response time: healthy AND the probe
	// is still inside its validity window. Computed rather than stored, because
	// it is a fact about now, not about the row — a client that stored it would
	// be wrong an hour later.
	Verified bool `json:"verified" example:"true"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ActivatedAt *time.Time `json:"activated_at"`
	RetiredAt   *time.Time `json:"retired_at"`
}

// ListConnectionsResponse wraps the collection so pagination can be added later
// without breaking clients.
type ListConnectionsResponse struct {
	Connections []ConnectionResponse `json:"connections"`
	Count       int                  `json:"count" example:"2"`
}

// VerifyResponse is the body of a verification run: the connection as it now
// stands, plus the per-check report that explains the verdict.
type VerifyResponse struct {
	Connection ConnectionResponse `json:"connection"`
	Report     VerifyReport       `json:"report"`
}

// CreateConnectionRequest is the POST body.
//
// `validate:"required"` is documentation for the OpenAPI generator, not a
// runtime check — the `binding` tag is deliberately absent so validation
// happens once, in the service, with the specific stable code for each field.
type CreateConnectionRequest struct {
	Name string `json:"name" validate:"required" example:"Production Keycloak"`
	// Provider is optional while only one exists; omitted means keycloak.
	Provider string `json:"provider,omitempty" example:"keycloak"`
	BaseURL  string `json:"base_url" validate:"required" example:"https://kc.example.com"`
	Realm    string `json:"realm" validate:"required" example:"saas"`
	ClientID string `json:"client_id" validate:"required" example:"saas-backend-admin"`
	// ClientSecret is write-only. It is sealed with AES-256-GCM before it
	// reaches the database and is never returned by any endpoint.
	ClientSecret string `json:"client_secret" validate:"required" example:"the-service-account-secret"`
}

// UpdateConnectionRequest is the PATCH body. Every field is optional; absent
// means unchanged.
//
// Status is declared but not settable: state changes go through the activate
// and retire operations. Declaring it is what lets the handler answer "status
// is immutable" instead of silently ignoring the field.
type UpdateConnectionRequest struct {
	Name         *string `json:"name,omitempty"`
	BaseURL      *string `json:"base_url,omitempty"`
	Realm        *string `json:"realm,omitempty"`
	ClientID     *string `json:"client_id,omitempty"`
	ClientSecret *string `json:"client_secret,omitempty"`
	Status       *string `json:"status,omitempty"`
	Provider     *string `json:"provider,omitempty"`
}

// ErrorResponse is the stable /v1 error envelope, identical to the workspace
// domain's — the surface has one error contract.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the stable code, prose, and the request id tying the
// response to the log line that holds the real cause.
type ErrorBody struct {
	Code      string `json:"code"       example:"connection_not_verified"`
	Message   string `json:"message"    example:"Connection must pass verification before it can be activated"`
	RequestID string `json:"request_id" example:"3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
}

// toResponse converts a domain connection to its wire form. now is passed in so
// the computed Verified field is evaluated against the same clock the service
// uses.
func toResponse(c *Connection, now time.Time) ConnectionResponse {
	return ConnectionResponse{
		ID:              c.PublicID(),
		WorkspaceID:     c.WorkspacePublicID(),
		Name:            c.Name,
		Provider:        string(c.Provider),
		Status:          string(c.Status),
		BaseURL:         c.BaseURL,
		Realm:           c.Realm,
		ClientID:        c.ClientID,
		HasClientSecret: true,
		Health:          string(c.Health),
		HealthMessage:   c.HealthMessage,
		AccessMode:      string(c.AccessMode),
		CanWrite:        c.AccessMode.CanWrite(),
		LastVerifiedAt:  c.LastVerifiedAt,
		Verified:        c.IsVerified(now),
		CreatedAt:       c.CreatedAt,
		UpdatedAt:       c.UpdatedAt,
		ActivatedAt:     c.ActivatedAt,
		RetiredAt:       c.RetiredAt,
	}
}

// toListResponse converts a slice. Connections is always a non-nil slice so an
// empty result marshals as `[]`, not `null`.
func toListResponse(items []Connection, now time.Time) ListConnectionsResponse {
	out := make([]ConnectionResponse, 0, len(items))
	for i := range items {
		out = append(out, toResponse(&items[i], now))
	}
	return ListConnectionsResponse{Connections: out, Count: len(out)}
}
