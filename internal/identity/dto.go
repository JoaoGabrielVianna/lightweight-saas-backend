package identity

import "time"

// UserResponse is the wire shape of a single user returned by /users and
// /users/:id. Snake-cased + JSON-tagged independently of the provider's
// struct so the public API contract doesn't drift if we switch providers.
type UserResponse struct {
	ID            string              `json:"id"`
	Username      string              `json:"username"`
	Email         string              `json:"email"`
	FirstName     string              `json:"first_name"`
	LastName      string              `json:"last_name"`
	Enabled       bool                `json:"enabled"`
	EmailVerified bool                `json:"email_verified"`
	CreatedAt     time.Time           `json:"created_at"`
	Attributes    map[string][]string `json:"attributes,omitempty"`
}

// ListUsersResponse is the wire shape for GET /users. `first` and `max`
// are echoed back so the client knows which page it's on without parsing
// query strings.
type ListUsersResponse struct {
	Users []UserResponse `json:"users"`
	First int            `json:"first"`
	Max   int            `json:"max"`
	Count int            `json:"count"` // length of users array, NOT total
}

// toUserResponse projects the provider-agnostic User onto the wire shape.
func toUserResponse(u User) UserResponse {
	return UserResponse{
		ID:            u.ID,
		Username:      u.Username,
		Email:         u.Email,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Enabled:       u.Enabled,
		EmailVerified: u.EmailVerified,
		CreatedAt:     u.CreatedAt,
		Attributes:    u.Attributes,
	}
}

// ─────────────── Roles ───────────────

type RoleResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Composite   bool   `json:"composite"`
	Builtin     bool   `json:"builtin"`
}

type ListRolesResponse struct {
	Roles []RoleResponse `json:"roles"`
	Count int            `json:"count"`
}

func toRoleResponse(r Role) RoleResponse {
	return RoleResponse{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		Composite:   r.Composite,
		Builtin:     r.Builtin,
	}
}

// ─────────────── Sessions ───────────────

type SessionResponse struct {
	ID         string            `json:"id"`
	UserID     string            `json:"user_id"`
	Username   string            `json:"username"`
	IPAddress  string            `json:"ip_address"`
	UserAgent  string            `json:"user_agent,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	LastAccess time.Time         `json:"last_access"`
	Clients    map[string]string `json:"clients,omitempty"`
}

type ListSessionsResponse struct {
	Sessions []SessionResponse `json:"sessions"`
	Count    int               `json:"count"`
}

func toSessionResponse(s Session) SessionResponse {
	return SessionResponse{
		ID:         s.ID,
		UserID:     s.UserID,
		Username:   s.Username,
		IPAddress:  s.IPAddress,
		UserAgent:  s.UserAgent,
		StartedAt:  s.StartedAt,
		LastAccess: s.LastAccess,
		Clients:    s.Clients,
	}
}

// ─────────────── Invitations ───────────────

type InvitationResponse struct {
	ID              string    `json:"id"`
	Email           string    `json:"email"`
	Username        string    `json:"username"`
	RequiredActions []string  `json:"required_actions"`
	InvitedBy       string    `json:"invited_by,omitempty"`
	ExpiresAt       string    `json:"expires_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Status          string    `json:"status"`
}

type ListInvitationsResponse struct {
	Invitations []InvitationResponse `json:"invitations"`
	Count       int                  `json:"count"`
}

func toInvitationResponse(i Invitation) InvitationResponse {
	return InvitationResponse{
		ID:              i.ID,
		Email:           i.Email,
		Username:        i.Username,
		RequiredActions: i.RequiredActions,
		InvitedBy:       i.InvitedBy,
		ExpiresAt:       i.ExpiresAt,
		CreatedAt:       i.CreatedAt,
		Status:          i.Status,
	}
}

// ─── Stage 5.2B — CREATE request shapes (HTTP body) ──────────────────────

// CreateRoleRequestBody is the HTTP shape for POST /admin/roles. Kept
// distinct from the provider's CreateRoleRequest so the wire format is free
// to evolve without churn on the provider interface (e.g. add composites
// in a future stage).
type CreateRoleRequestBody struct {
	Name        string `json:"name"        example:"support"`
	Description string `json:"description" example:"Support team role"`
}

// CreateInvitationRequestBody is the HTTP shape for POST /admin/invitations
// and its alias POST /admin/users/invite.
type CreateInvitationRequestBody struct {
	Email     string   `json:"email"      example:"user@example.com"`
	FirstName string   `json:"first_name" example:"Jane"`
	LastName  string   `json:"last_name"  example:"Doe"`
	Roles     []string `json:"roles"      example:"user"`
	ExpiresAt string   `json:"expires_at,omitempty" example:"2026-12-31T23:59:59Z"`
	InvitedBy string   `json:"invited_by,omitempty" example:"adminuser"`
}

// ─── Stage 5.2C — UPDATE request shapes ──────────────────────────────────

// UpdateUserRequestBody is the HTTP shape for PATCH /admin/users/:id.
// Pointer fields distinguish "client omitted" from "client set to zero".
// Omitted fields are preserved by the service+provider read-modify-write.
type UpdateUserRequestBody struct {
	FirstName *string `json:"first_name,omitempty" example:"Jane"`
	LastName  *string `json:"last_name,omitempty"  example:"Doe"`
	Email     *string `json:"email,omitempty"      example:"jane@example.com"`
	Enabled   *bool   `json:"enabled,omitempty"    example:"true"`
}

// UpdateRoleRequestBody is the HTTP shape for PATCH /admin/roles/:name.
// Only description is mutable; renaming is intentionally not exposed.
type UpdateRoleRequestBody struct {
	Description *string `json:"description,omitempty" example:"Updated support team description"`
}

// AssignRolesRequestBody is the HTTP shape for POST /admin/users/:id/roles.
// Each entry is a role NAME (Keycloak's natural key); the service resolves
// them to id+name internally before calling the Admin API.
type AssignRolesRequestBody struct {
	Roles []string `json:"roles" example:"editor"`
}
