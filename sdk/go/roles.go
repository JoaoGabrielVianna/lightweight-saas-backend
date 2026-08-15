package lightweight

import (
	"context"
	"net/http"
)

// RolesService administers roles and their assignment to users.
//
// Roles and user-role grants live on ONE service even though the grants are
// addressed under /users, because they share an authorization boundary: both are
// governed by roles:read and roles:write, not by users:*. That split is
// deliberate on the server's part — an operator can let a backend manage
// profiles without also letting it hand out privileges — and grouping by scope
// rather than by URL keeps it visible from here.
//
// Obtain it from [Client.Roles].
type RolesService struct {
	client *Client
}

// roleListEnvelope is the wire shape of the unpaginated role collections.
//
// Unexported, and unwrapped before returning, because the `count` it carries is
// always len(roles) — the server documents it as the page length, and these
// endpoints return everything. Publishing a struct whose only extra field is a
// number the caller can compute would invite it to be read as a total.
type roleListEnvelope struct {
	Roles []Role `json:"roles"`
}

// List returns every role defined in the workspace.
//
// The collection is complete and unpaginated. Builtin roles are included and are
// marked by [Role.Builtin].
//
// Required scope: roles:read.
func (s *RolesService) List(ctx context.Context) ([]Role, error) {
	const op = "Roles.List"
	path := s.client.workspacePath("roles")

	var out roleListEnvelope
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Roles, nil
}

// Get returns one role by name.
//
// Returns an [APIError] with Code [CodeRoleNotFound] when the workspace has no
// such role.
//
// Required scope: roles:read.
func (s *RolesService) Get(ctx context.Context, name string) (*Role, error) {
	const op = "Roles.Get"
	if err := requiredArg(op, "name", name); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("roles", name)

	var out Role
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateRoleRequest is the body of [RolesService.Create].
type CreateRoleRequest struct {
	// Name is required and is the role's permanent natural key. It cannot be
	// changed afterwards: every grant references it by name, so renaming would
	// mean rewriting them all.
	Name string `json:"name"`

	// Description is free prose shown to operators. Optional, and unlike a
	// patch, absent and empty mean the same thing here.
	Description string `json:"description,omitempty"`
}

// Create defines a new role and returns it.
//
// An existing name is refused with [CodeRoleAlreadyExists]; a name belonging to
// the platform is refused with [CodeRoleReserved].
//
// Required scope: roles:write.
func (s *RolesService) Create(ctx context.Context, req CreateRoleRequest) (*Role, error) {
	const op = "Roles.Create"
	path := s.client.workspacePath("roles")

	var out Role
	if err := s.client.do(ctx, op, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRoleRequest is the body of [RolesService.Update].
//
// Description is the only mutable field, so it is the only field here. A
// pointer, because this is a patch and clearing a description must be
// expressible.
type UpdateRoleRequest struct {
	Description *string `json:"description,omitempty"`
}

// Update changes a role's description and returns the updated role.
//
// Protected platform roles are refused with [CodeRoleReserved].
//
// Required scope: roles:write.
func (s *RolesService) Update(ctx context.Context, name string, req UpdateRoleRequest) (*Role, error) {
	const op = "Roles.Update"
	if err := requiredArg(op, "name", name); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("roles", name)

	var out Role
	if err := s.client.do(ctx, op, http.MethodPatch, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a role from the workspace.
//
// IRREVERSIBLE, and it revokes the role from every user who holds it. There is
// no per-user confirmation and no listing of who was affected: call
// [RolesService.ListUsers] first if you need to know.
//
// Protected platform roles are refused with [CodeRoleReserved].
//
// Required scope: roles:write.
func (s *RolesService) Delete(ctx context.Context, name string) error {
	const op = "Roles.Delete"
	if err := requiredArg(op, "name", name); err != nil {
		return err
	}
	path := s.client.workspacePath("roles", name)
	return s.client.do(ctx, op, http.MethodDelete, path, nil, nil, nil)
}

// ListUsers returns the users holding a role.
//
// It answers with the same [UserPage] shape as [UsersService.List], including
// its offset fields, because it is the same collection filtered.
//
// Required scope: roles:read.
func (s *RolesService) ListUsers(ctx context.Context, name string) (*UserPage, error) {
	const op = "Roles.ListUsers"
	if err := requiredArg(op, "name", name); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("roles", name, "users")

	var out UserPage
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListForUser returns the roles granted to one user.
//
// Required scope: roles:read. Note that this is roles:read and NOT users:read —
// reading someone's privileges is classified with privileges.
func (s *RolesService) ListForUser(ctx context.Context, userID string) ([]Role, error) {
	const op = "Roles.ListForUser"
	if err := requiredArg(op, "userID", userID); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("users", userID, "roles")

	var out roleListEnvelope
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Roles, nil
}

// grantRolesRequest is the wire body for a grant.
type grantRolesRequest struct {
	Roles []string `json:"roles"`
}

// Grant gives a user one or more roles by name.
//
// Additive: roles the user already holds are unaffected, and roles not named
// here are not removed. Granting is not idempotent-by-accident either — the
// server accepts a repeat without complaint.
//
// A Project Credential CANNOT grant administrative roles, whatever scopes it
// holds; those attempts are refused with [CodeRolePrivileged]. That bound is
// what stops roles:write being an escalation to full administration, so it is
// not something an operator can widen with a different key.
//
// Required scope: roles:write.
func (s *RolesService) Grant(ctx context.Context, userID string, roleNames ...string) error {
	const op = "Roles.Grant"
	if err := requiredArg(op, "userID", userID); err != nil {
		return err
	}
	if len(roleNames) == 0 {
		// A no-op call that still costs a round trip and an audit event is not
		// something to send on the caller's behalf.
		return requiredArg(op, "roleNames", "")
	}
	path := s.client.workspacePath("users", userID, "roles")
	return s.client.do(ctx, op, http.MethodPost, path, nil, grantRolesRequest{Roles: roleNames}, nil)
}

// Revoke takes one role away from one user.
//
// Singular by design: this mirrors the API exactly, which has no bulk revoke, so
// there is no method here that could remove several privileges in a single call
// a reviewer might skim past.
//
// Revoking an administrative role is refused with [CodeRolePrivileged], as
// granting one is.
//
// Required scope: roles:write.
func (s *RolesService) Revoke(ctx context.Context, userID, roleName string) error {
	const op = "Roles.Revoke"
	if err := requiredArg(op, "userID", userID); err != nil {
		return err
	}
	if err := requiredArg(op, "roleName", roleName); err != nil {
		return err
	}
	path := s.client.workspacePath("users", userID, "roles", roleName)
	return s.client.do(ctx, op, http.MethodDelete, path, nil, nil, nil)
}
