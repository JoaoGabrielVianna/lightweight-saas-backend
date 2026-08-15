package identityruntime

import (
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/gin-gonic/gin"
)

// guardPrivilegedRoles refuses a MACHINE credential touching a protected role.
//
// # The gap this closes
//
// identity.Service protects reserved role names on create, update and delete —
// every caller, operator or machine, is refused there. It does NOT protect role
// ASSIGNMENT: `POST .../users/{id}/roles` with `{"roles":["admin"]}` succeeds,
// and by design, because an operator reaching that endpoint is already a live
// realm admin and granting admin is a normal thing for them to do.
//
// A project credential is a different principal with the same endpoint. Without
// this guard, `roles:write` would be an escalation path: grant `admin` in the
// workspace's realm to a user the backend controls. Where the workspace's realm
// and the installation's realm are the same Keycloak realm — the ordinary
// single-realm deployment — that is a grant of console-operator privilege to
// whoever holds the key.
//
// The guard is the smallest thing that closes it: machines may not name a
// protected role in a grant or a revoke. It adds no new role model, no
// hierarchy and no configuration; it reuses identity.IsProtectedRoleName so the
// list cannot drift from the one the service already enforces elsewhere.
//
// # What roles:write CAN do
//
//   - create, update and delete non-reserved realm roles;
//   - grant and revoke those roles on any user in the bound workspace's realm;
//   - read roles and their membership (with roles:read).
//
// # What roles:write CANNOT do
//
//   - create, update or delete `admin`, `user`, `offline_access`,
//     `uma_authorization` or `default-roles-*` — already refused by
//     identity.Service for every caller;
//   - GRANT or REVOKE any of those names — refused here, for machines only.
//
// Revocation is included deliberately, not just granting. A machine able to
// strip `admin` could lock every operator out of the realm it administers,
// which is a denial-of-service with the same root cause and the same fix.
//
// Returns true when the request may proceed. On refusal it has ALREADY written
// the response, matching the read/write seams' convention.
func (h *Handler) guardPrivilegedRoles(c *gin.Context, names ...string) bool {
	p, ok := auth.PrincipalFrom(c)
	// No principal, or an operator: unchanged behaviour. Operators are governed
	// by the service's own guards (no self-strip of admin, no removing the
	// realm's last admin), which stay exactly as they were.
	if !ok || p == nil || !p.IsProject() {
		return true
	}

	for _, name := range names {
		if identity.IsProtectedRoleName(name) {
			respondError(c, ErrRolePrivileged)
			return false
		}
	}
	return true
}
