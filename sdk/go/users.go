package lightweight

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// UsersService administers user records in the client's workspace.
//
// Obtain it from [Client.Users]; it is not constructed directly.
type UsersService struct {
	client *Client
}

// UserListOptions filters and pages [UsersService.List].
//
// This is OFFSET pagination — the only offset-paginated collection in this API.
// The directory can change between pages, so a user created during a walk may be
// seen twice or not at all. That is a property of the underlying store, not
// something this package can paper over, and it is why the audit trail uses a
// cursor instead.
type UserListOptions struct {
	// Search is a substring match over username, email, first and last name.
	// Empty means no filter.
	Search string

	// First is the offset to start from. Zero means the beginning, which is also
	// the server's default, so there is no way to express "not supplied"
	// separately and no need for one.
	First int

	// Max is the page size. Zero means the server's default (20). The server
	// clamps large values; [UserPage.Max] reports what was actually applied, so
	// read that rather than assuming this value was honoured.
	Max int
}

// query renders the options as a query string, omitting anything unset.
func (o UserListOptions) query() url.Values {
	q := url.Values{}
	if o.Search != "" {
		q.Set("search", o.Search)
	}
	if o.First > 0 {
		q.Set("first", strconv.Itoa(o.First))
	}
	if o.Max > 0 {
		q.Set("max", strconv.Itoa(o.Max))
	}
	return q
}

// List returns a page of users in the workspace.
//
// Required scope: users:read.
func (s *UsersService) List(ctx context.Context, opts UserListOptions) (*UserPage, error) {
	const op = "Users.List"
	path := s.client.workspacePath("users")

	var out UserPage
	if err := s.client.do(ctx, op, http.MethodGet, path, opts.query(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get returns one user by id.
//
// Returns an [APIError] with Code [CodeUserNotFound] when there is no such user
// in this workspace.
//
// Required scope: users:read.
func (s *UsersService) Get(ctx context.Context, userID string) (*User, error) {
	const op = "Users.Get"
	if err := requiredArg(op, "userID", userID); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("users", userID)

	var out User
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateUserRequest is the body of [UsersService.Create].
//
// This creates a user directly, with a credential you choose. The alternative is
// [InvitationsService.Create], which emails the user and lets them choose their
// own — preferable whenever the workspace can send mail, because it means no
// password ever passes through your service.
type CreateUserRequest struct {
	// Email is required, and becomes the user's username.
	Email string `json:"email"`

	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`

	// TemporaryPassword is required and must be at least 8 characters. The user
	// is forced to change it on first sign-in.
	//
	// It is never echoed back by the server, never logged by it, and never
	// recorded in the audit trail. On this side, [CreateUserRequest] redacts it
	// in String and GoString, so printing the request cannot leak it. What this
	// package cannot do is control the lifetime of the string in your process:
	// generate it, send it, hand it to the user, and do not persist it.
	TemporaryPassword string `json:"temporary_password"`

	// Roles are role names to grant at creation. Optional; roles can also be
	// granted afterwards with [RolesService.Grant].
	//
	// Granting a role needs roles:write IN ADDITION to users:write, because what
	// is sensitive is the privilege being handed out rather than the user
	// record. Administrative roles cannot be granted by a Project Credential at
	// all — see [CodeRolePrivileged].
	Roles []string `json:"roles,omitempty"`
}

// String renders the request with the temporary password redacted.
//
// Present for the same reason [Config.String] is: the default formatting of a
// struct holding a credential prints the credential, and a request struct is
// exactly the kind of value that ends up in a debug log or a wrapped error while
// someone is working out why creation failed.
func (r CreateUserRequest) String() string {
	return "lightweight.CreateUserRequest{Email:" + quote(r.Email) +
		", FirstName:" + quote(r.FirstName) +
		", LastName:" + quote(r.LastName) +
		", TemporaryPassword:<redacted>" +
		", Roles:" + joinQuoted(r.Roles) + "}"
}

// GoString covers %#v, which ignores String.
func (r CreateUserRequest) GoString() string { return r.String() }

// Create provisions a user and returns it.
//
// A duplicate email is refused with [CodeConflict].
//
// Required scope: users:write (plus roles:write when Roles is non-empty).
func (s *UsersService) Create(ctx context.Context, req CreateUserRequest) (*User, error) {
	const op = "Users.Create"
	var out User
	path := s.client.workspacePath("users")
	if err := s.client.do(ctx, op, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateUserRequest is the body of [UsersService.Update].
//
// Every field is a pointer, and here that is load-bearing rather than
// mechanical: this is a PATCH, so nil means "leave this alone" and a pointer to
// the zero value means "set it to empty/false". Plain fields could not express
// the difference, and disabling a user is exactly `Enabled: lightweight.Bool(false)`.
type UpdateUserRequest struct {
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`

	// Email changes the address. It does NOT change the username, which was
	// fixed at creation.
	Email *string `json:"email,omitempty"`

	// Enabled set to false stops the user authenticating without deleting them
	// or their role grants. This is the reversible half of [UsersService.Delete].
	Enabled *bool `json:"enabled,omitempty"`

	// EmailVerified marks the address confirmed, for callers that verify
	// addresses themselves.
	EmailVerified *bool `json:"email_verified,omitempty"`
}

// Update applies a partial change and returns the updated user.
//
// Required scope: users:write.
func (s *UsersService) Update(ctx context.Context, userID string, req UpdateUserRequest) (*User, error) {
	const op = "Users.Update"
	if err := requiredArg(op, "userID", userID); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("users", userID)

	var out User
	if err := s.client.do(ctx, op, http.MethodPatch, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete permanently removes a user.
//
// IRREVERSIBLE, and it destroys the user's role grants and sessions with them.
// To stop someone signing in while keeping the account, set Enabled to false
// with [UsersService.Update] instead.
//
// Deleting an already-absent user is refused with [CodeUserNotFound] rather
// than treated as success; this package does not soften that, because a caller
// that cannot tell "deleted" from "was never there" cannot detect an id mix-up.
//
// Required scope: users:write.
func (s *UsersService) Delete(ctx context.Context, userID string) error {
	const op = "Users.Delete"
	if err := requiredArg(op, "userID", userID); err != nil {
		return err
	}
	path := s.client.workspacePath("users", userID)
	return s.client.do(ctx, op, http.MethodDelete, path, nil, nil, nil)
}

// SendPasswordReset emails the user a link to choose a new password.
//
// Nothing is changed by this call: the user's existing credential keeps working
// until they complete the flow, and the new one is chosen by them, so it never
// passes through your service. It requires the workspace to be able to send
// mail; a workspace that cannot is refused by the server rather than silently
// doing nothing.
//
// This is the ONLY password operation a Project Credential can perform. Setting
// a password directly is restricted to console operators by design — it is a
// complete account-takeover primitive, and this flow covers the legitimate need
// without one. There is deliberately no method here for it.
//
// Required scope: users:write.
func (s *UsersService) SendPasswordReset(ctx context.Context, userID string) error {
	const op = "Users.SendPasswordReset"
	if err := requiredArg(op, "userID", userID); err != nil {
		return err
	}
	path := s.client.workspacePath("users", userID, "reset-password")
	return s.client.do(ctx, op, http.MethodPost, path, nil, nil, nil)
}

// Bool returns a pointer to v, for the pointer fields of [UpdateUserRequest].
//
//	client.Users.Update(ctx, id, lightweight.UpdateUserRequest{
//		Enabled: lightweight.Bool(false),
//	})
func Bool(v bool) *bool { return &v }

// String returns a pointer to v, for the pointer fields of [UpdateUserRequest].
func String(v string) *string { return &v }
