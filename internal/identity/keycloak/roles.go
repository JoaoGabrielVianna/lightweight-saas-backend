package keycloak

import (
	"context"
	"net/url"
	"strconv"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// usersByRolePageSize bounds how many users we fetch in one Keycloak call
// from /roles/:name/users. The endpoint shares the same first/max
// pagination shape as /users, so we mirror the strategy used by
// ListInvitations: page until a short page or the hard cap.
//
// Pre-pagination this method sent no first/max — Keycloak defaulted to 100,
// which silently truncated roles with more members. Critical for the admin
// role specifically because assertNotLastAdmin in the service tier consumes
// the full set to decide whether deletion would leave the realm admin-less.
const usersByRolePageSize = 200

// usersByRoleHardCap is the maximum number of users ListUsersByRole will
// scan in a single call. Same rationale as invitationsHardCap: bounds
// pagination time and defends against a runaway upstream.
const usersByRoleHardCap = 10000

// kcRole mirrors Keycloak's RoleRepresentation. Keep the field set narrow —
// extra fields Keycloak may add in future versions just go ignored.
type kcRole struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Composite   bool   `json:"composite"`
	ClientRole  bool   `json:"clientRole"`
	ContainerID string `json:"containerId"`
}

// builtinRoleNames are realm-management roles Keycloak ships with every
// realm. We mark them as Builtin in the API so the UI can disable destructive
// actions against them. The list intentionally covers only the names every
// supported Keycloak version uses; future Keycloak releases adding more will
// be classified as user-managed (safer default than incorrectly marking a
// user-created role as built-in).
var builtinRoleNames = map[string]struct{}{
	"offline_access":       {},
	"uma_authorization":    {},
	"default-roles-saas":   {},
	"default-roles-master": {},
}

func (u kcRole) toIdentity() identity.Role {
	// default-roles-<realm> is a per-realm role; check prefix in addition
	// to the static list above to catch all of them.
	_, builtin := builtinRoleNames[u.Name]
	if !builtin && len(u.Name) > len("default-roles-") && u.Name[:len("default-roles-")] == "default-roles-" {
		builtin = true
	}
	return identity.Role{
		ID:          u.ID,
		Name:        u.Name,
		Description: u.Description,
		Composite:   u.Composite,
		Builtin:     builtin,
	}
}

// ListRoles returns every realm role.
func (p *Provider) ListRoles(ctx context.Context) ([]identity.Role, error) {
	var raw []kcRole
	if err := p.client.doJSON(ctx, "GET", "/roles", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]identity.Role, 0, len(raw))
	for _, r := range raw {
		// Skip client roles — this surface is realm-only.
		if r.ClientRole {
			continue
		}
		out = append(out, r.toIdentity())
	}
	return out, nil
}

// GetRole fetches a single realm role by name. Role names are Keycloak's
// natural key; the URL must be path-escaped because names can contain
// characters like spaces (operators sometimes use them in display names).
func (p *Provider) GetRole(ctx context.Context, name string) (*identity.Role, error) {
	var raw kcRole
	if err := p.client.doJSON(ctx, "GET", "/roles/"+url.PathEscape(name), nil, nil, &raw); err != nil {
		return nil, err
	}
	r := raw.toIdentity()
	return &r, nil
}

// ListUsersByRole returns every user carrying a given realm role. Pages
// through Keycloak's /roles/:name/users using first/max until a short
// page or the hard cap. Without this loop the admin-role lookup in
// assertNotLastAdmin silently truncated at 100 users — a realm with >100
// admins could lose its last admin because the guard didn't see the rest.
func (p *Provider) ListUsersByRole(ctx context.Context, role string) ([]identity.User, error) {
	out := make([]identity.User, 0)

	for first := 0; first < usersByRoleHardCap; first += usersByRolePageSize {
		params := url.Values{}
		params.Set("first", strconv.Itoa(first))
		params.Set("max", strconv.Itoa(usersByRolePageSize))

		var raw []kcUser
		if err := p.client.doJSON(ctx, "GET", "/roles/"+url.PathEscape(role)+"/users", params, nil, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}
		for _, u := range raw {
			out = append(out, u.toIdentity())
		}
		if len(raw) < usersByRolePageSize {
			break
		}
	}
	return out, nil
}

// ListUserRoles returns the realm roles assigned to a user (the "effective"
// realm role-mapping, not the merge with composite roles).
func (p *Provider) ListUserRoles(ctx context.Context, userID string) ([]identity.Role, error) {
	var raw []kcRole
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(userID)+"/role-mappings/realm", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]identity.Role, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.toIdentity())
	}
	return out, nil
}

// ─── Stage 5.2B — CREATE ──────────────────────────────────────────────────

// kcCreateRoleBody is the POST body Keycloak expects for new realm roles.
// We marshal a struct (not map[string]any) so missing fields are omitted
// cleanly rather than serialized as nil/zero.
type kcCreateRoleBody struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CreateRole creates a new realm role. Keycloak's POST /roles returns 201
// with no body; we follow up with GET /roles/<name> so the caller gets the
// authoritative server-side representation (including the new id).
//
// Duplicate-name errors surface as identity.ErrConflict via the admin
// client's status mapping. The service layer is responsible for rejecting
// disallowed names (built-ins) BEFORE we ever call this — the provider is
// permissive on purpose so the policy lives in exactly one place.
func (p *Provider) CreateRole(ctx context.Context, req identity.CreateRoleRequest) (*identity.Role, error) {
	body := kcCreateRoleBody{Name: req.Name, Description: req.Description}
	if _, err := p.client.doCreate(ctx, "/roles", body); err != nil {
		// CreateRole's Location header points to /admin/realms/<realm>/roles-by-id/<uuid>,
		// but we re-fetch by name below so we don't depend on it.
		return nil, err
	}
	return p.GetRole(ctx, req.Name)
}

// ─── Stage 5.2C — UPDATE ──────────────────────────────────────────────────

// kcUpdateRoleBody is the PUT body for /roles/:name. We carry name verbatim
// (Keycloak requires it in the body for PUT) and the merged description.
type kcUpdateRoleBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// UpdateRole patches a role. Only description is mutable here. We GET first
// so an empty/omitted description in the request doesn't blank the existing
// one — same merge contract as UpdateUser.
func (p *Provider) UpdateRole(ctx context.Context, name string, req identity.UpdateRoleRequest) (*identity.Role, error) {
	var current kcRole
	if err := p.client.doJSON(ctx, "GET", "/roles/"+url.PathEscape(name), nil, nil, &current); err != nil {
		return nil, err
	}
	body := kcUpdateRoleBody{Name: current.Name, Description: current.Description}
	if req.Description != nil {
		body.Description = *req.Description
	}
	if err := p.client.doJSON(ctx, "PUT", "/roles/"+url.PathEscape(name), nil, body, nil); err != nil {
		return nil, err
	}
	return p.GetRole(ctx, name)
}

// resolveRoles turns a slice of role names into the {id,name} payload
// Keycloak's role-mapping endpoints require. Every name must resolve — a
// single 404 short-circuits the whole operation so we never half-apply.
func (p *Provider) resolveRoles(ctx context.Context, names []string) ([]kcRoleBrief, error) {
	out := make([]kcRoleBrief, 0, len(names))
	for _, n := range names {
		r, err := p.GetRole(ctx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, kcRoleBrief{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

// AssignRolesToUser grants the named realm roles. Pre-resolves names so the
// 404 case never half-applies. Empty slices are a no-op.
func (p *Provider) AssignRolesToUser(ctx context.Context, userID string, roles []string) error {
	if len(roles) == 0 {
		return nil
	}
	resolved, err := p.resolveRoles(ctx, roles)
	if err != nil {
		return err
	}
	return p.client.doJSON(ctx, "POST", "/users/"+url.PathEscape(userID)+"/role-mappings/realm", nil, resolved, nil)
}

// UnassignRolesFromUser removes the named realm roles. Keycloak's DELETE
// /users/:id/role-mappings/realm accepts a JSON body of role briefs —
// doJSON happily sends bodies with DELETE.
func (p *Provider) UnassignRolesFromUser(ctx context.Context, userID string, roles []string) error {
	if len(roles) == 0 {
		return nil
	}
	resolved, err := p.resolveRoles(ctx, roles)
	if err != nil {
		return err
	}
	return p.client.doJSON(ctx, "DELETE", "/users/"+url.PathEscape(userID)+"/role-mappings/realm", nil, resolved, nil)
}

// ─── Stage 5.2D — DELETE ──────────────────────────────────────────────────

// DeleteRole removes a realm role. Keycloak rejects deletion of its built-in
// roles (offline_access, uma_authorization, default-roles-*) with 400; the
// service tier short-circuits those before we get here, so a 400 reaching
// this path means a Keycloak-internal protected role we didn't anticipate.
func (p *Provider) DeleteRole(ctx context.Context, name string) error {
	return p.client.doJSON(ctx, "DELETE", "/roles/"+url.PathEscape(name), nil, nil, nil)
}
