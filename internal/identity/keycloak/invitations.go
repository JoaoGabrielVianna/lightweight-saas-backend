package keycloak

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// invitationsPageSize bounds how many users we scan in ONE Keycloak call.
// The pagination loop walks pages of this size until Keycloak returns a
// short page or the hard cap fires.
const invitationsPageSize = 200

// invitationsHardCap is the maximum number of users ListInvitations will
// scan in total across a single call. Realms approaching this size should
// adopt the v0.3 local-mirror approach (issue tracker: identity v0.3).
//
// Rationale for the value: at 200/page, this is 50 sequential round-trips
// to Keycloak. With a typical realm RTT of ~50ms that's a 2.5s ceiling on
// pagination time — under the AdminClient's per-request timeout but already
// well past what an interactive admin should wait for.
//
// The cap also defends against a buggy upstream that returns full pages
// forever (covered by TestListInvitations_HardCap_PreventsRunaway).
const invitationsHardCap = 10000

// compensatingDeleteTimeout caps how long the best-effort cleanup of a
// half-provisioned user can take. The caller's context may already be
// cancelled when we reach the cleanup path (that's often WHY we're
// cleaning up), so we use a fresh background context with this timeout
// rather than re-using ctx.
const compensatingDeleteTimeout = 5 * time.Second

// kcUserWithActions extends kcUser with the requiredActions slice — the
// canonical signal that distinguishes an invited-but-incomplete user from
// a fully-onboarded one.
type kcUserWithActions struct {
	kcUser
	RequiredActions []string `json:"requiredActions"`
}

// ListInvitations synthesizes an invitation list from Keycloak users whose
// state signals "invited but not yet completed":
//
//   - users carrying any required action (UPDATE_PASSWORD, VERIFY_EMAIL,
//     CONFIGURE_TOTP, etc.) — they were created via the invite flow OR
//     an admin manually queued required actions
//   - users with the `invited_by` user-attribute set (Stage B will write
//     this) — even after required actions clear, they're recognizable
//     until an admin removes the attribute
//
// Disabled users are surfaced too — they may have been invited and then
// revoked. The Status field distinguishes the variants.
//
// Pagination: Keycloak doesn't filter on requiredActions server-side, so
// we page through /users with first/max and apply the predicate in-process.
// The loop terminates on a short page (the natural end of the dataset) or
// at invitationsHardCap (defensive ceiling). For realms above the cap,
// adopt the v0.3 local-mirror approach.
func (p *Provider) ListInvitations(ctx context.Context) ([]identity.Invitation, error) {
	out := make([]identity.Invitation, 0)

	for first := 0; first < invitationsHardCap; first += invitationsPageSize {
		params := url.Values{}
		params.Set("first", strconv.Itoa(first))
		params.Set("max", strconv.Itoa(invitationsPageSize))

		var raw []kcUserWithActions
		if err := p.client.doJSON(ctx, "GET", "/users", params, nil, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}

		for _, u := range raw {
			invitedBy := firstAttr(u.Attributes, "invited_by")
			expiresAt := firstAttr(u.Attributes, "expires_at")

			hasRequired := len(u.RequiredActions) > 0
			hasInviteAttr := invitedBy != ""
			if !hasRequired && !hasInviteAttr {
				continue
			}

			out = append(out, identity.Invitation{
				ID:              u.ID,
				Email:           u.Email,
				Username:        u.Username,
				RequiredActions: u.RequiredActions,
				InvitedBy:       invitedBy,
				ExpiresAt:       expiresAt,
				CreatedAt:       u.toIdentity().CreatedAt,
				Status:          deriveInvitationStatus(u.Enabled, hasRequired, expiresAt),
			})
		}

		// Short page = end of dataset. Saves one round-trip on the
		// common case where the realm has fewer than invitationsHardCap
		// users.
		if len(raw) < invitationsPageSize {
			break
		}
	}
	return out, nil
}

func firstAttr(attrs map[string][]string, key string) string {
	if attrs == nil {
		return ""
	}
	v, ok := attrs[key]
	if !ok || len(v) == 0 {
		return ""
	}
	return v[0]
}

// deriveInvitationStatus maps Keycloak user state to one of the four canonical
// values (identity.InvitationStatus*). Precedence matters:
//
//  1. disabled → revoked (terminal, opt-out by admin)
//  2. enabled + no pending actions → accepted (terminal; expires_at is
//     irrelevant for an already-completed invitation — without this guard
//     an accepted invite whose expires_at later drifts into the past would
//     be wrongly reported as expired)
//  3. enabled + pending actions + expires_at in the past → expired
//  4. else → pending
//
// Unparseable expires_at values fall through to "pending" rather than
// surfacing an error — bad attribute data shouldn't poison the listing.
func deriveInvitationStatus(enabled, hasRequired bool, expiresAt string) string {
	if !enabled {
		return identity.InvitationStatusRevoked
	}
	if !hasRequired {
		// Accepted is terminal — don't consult expires_at. The invitation
		// completed, so any expiry that has since elapsed is meaningless.
		return identity.InvitationStatusAccepted
	}
	if expiresAt != "" {
		// Try a couple of formats Stage B is likely to write.
		for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
			if t, err := time.Parse(layout, expiresAt); err == nil {
				if time.Now().After(t) {
					return identity.InvitationStatusExpired
				}
				break
			}
		}
	}
	return identity.InvitationStatusPending
}

// ─── Stage 5.2B — CREATE ──────────────────────────────────────────────────

// requiredActionsForInvite is the canonical set every invited user receives.
// Stage B writes both so the user must verify their email AND set their
// password before the account is usable.
var requiredActionsForInvite = []string{"VERIFY_EMAIL", "UPDATE_PASSWORD"}

// kcCreateUserBody is the POST body Keycloak expects on /users for invite
// flows. We pin enabled=true so the user can complete onboarding without an
// admin re-enabling them, and emailVerified=false so VERIFY_EMAIL has a
// non-trivial effect.
type kcCreateUserBody struct {
	Username        string              `json:"username"`
	Email           string              `json:"email"`
	FirstName       string              `json:"firstName,omitempty"`
	LastName        string              `json:"lastName,omitempty"`
	Enabled         bool                `json:"enabled"`
	EmailVerified   bool                `json:"emailVerified"`
	RequiredActions []string            `json:"requiredActions,omitempty"`
	Attributes      map[string][]string `json:"attributes,omitempty"`
}

// kcRoleBrief is the minimal role shape Keycloak accepts in role-mapping
// payloads — both id and name are required by the Admin API.
type kcRoleBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CreateInvitation provisions an invited user end-to-end with compensating
// rollback. Ordering matters:
//
//  1. Pre-resolve every requested role (404 here means no user is created).
//  2. POST /users to create the principal.
//  3. POST /role-mappings/realm to grant the realm roles.
//  4. PUT /execute-actions-email to dispatch the invite email.
//  5. GET /users/:id to capture the final representation.
//
// Reliability invariant: if steps 3 or 4 fail after step 2 succeeded, we
// best-effort DELETE the half-provisioned user so the caller can retry with
// the same email. Without this, a transient role-mapping or SMTP failure
// would leave the realm in a state where every subsequent retry returns
// 409 Conflict on the email.
//
// Step 5 is treated as informational only — if it fails, the invitation IS
// provisioned (user exists, roles assigned, email dispatched), so we
// synthesize a response from what we know rather than rolling back work the
// user can already see in their inbox.
func (p *Provider) CreateInvitation(ctx context.Context, req identity.CreateInvitationRequest) (*identity.Invitation, error) {
	// 1. Pre-resolve every requested role. A failed lookup at this point
	//    yields 404 to the caller (no user created yet).
	roleObjects := make([]kcRoleBrief, 0, len(req.Roles))
	for _, name := range req.Roles {
		r, err := p.GetRole(ctx, name)
		if err != nil {
			return nil, err
		}
		roleObjects = append(roleObjects, kcRoleBrief{ID: r.ID, Name: r.Name})
	}

	// 2. Build the user representation. Username defaults to email — the
	//    realm import sets loginWithEmailAllowed=true so the user can log
	//    in by email; the username field still needs *some* value.
	attrs := map[string][]string{}
	if req.InvitedBy != "" {
		attrs["invited_by"] = []string{req.InvitedBy}
	}
	if req.ExpiresAt != "" {
		attrs["expires_at"] = []string{req.ExpiresAt}
	}

	body := kcCreateUserBody{
		Username:        req.Email,
		Email:           req.Email,
		FirstName:       req.FirstName,
		LastName:        req.LastName,
		Enabled:         true,
		EmailVerified:   false,
		RequiredActions: requiredActionsForInvite,
		Attributes:      attrs,
	}

	// 3. Create — Keycloak responds 201 with Location: /admin/realms/<r>/users/<uuid>.
	userID, err := p.client.doCreate(ctx, "/users", body)
	if err != nil {
		return nil, err
	}

	// Compensating-delete guard. `committed` is set true only after the
	// invite is fully provisioned (user + roles + email). If we exit
	// before that with an error, the defer cleans up the orphan so the
	// caller can retry with the same email.
	var committed bool
	defer func() {
		if committed {
			return
		}
		p.compensateInvitationCreate(userID)
	}()

	// 4. Assign the realm roles. On failure we surface the error; the
	//    defer above rolls back the user so the realm doesn't accumulate
	//    roleless orphans on transient role-mapping failures.
	if len(roleObjects) > 0 {
		if err := p.client.doJSON(ctx, "POST", "/users/"+url.PathEscape(userID)+"/role-mappings/realm", nil, roleObjects, nil); err != nil {
			return nil, err
		}
	}

	// 5. Dispatch the action email. Keycloak sends both VERIFY_EMAIL and
	//    UPDATE_PASSWORD in a single email when the realm is configured
	//    with SMTP. If SMTP is unconfigured this returns 500; the doJSON
	//    layer maps that to ErrAdminAPIUnavailable. The compensating
	//    delete above ensures we don't leave a user whose invite email
	//    was never sent (which would be invisible to the admin).
	if err := p.client.doJSON(ctx, "PUT", "/users/"+url.PathEscape(userID)+"/execute-actions-email", nil, requiredActionsForInvite, nil); err != nil {
		return nil, err
	}

	// Past this point the invitation is fully provisioned. The trailing
	// GET is informational — failures from here do NOT roll back work the
	// user can already see (the email is already in their inbox).
	committed = true

	// 6. Re-fetch — we GET so the response shape matches what
	//    ListInvitations would return for this row. On failure, synthesize
	//    a representation from the request: the create succeeded, so
	//    returning an error here would lose work and confuse retries.
	var u kcUserWithActions
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(userID), nil, nil, &u); err != nil {
		return p.synthesizeFreshInvitation(userID, req), nil
	}

	inv := identity.Invitation{
		ID:              u.ID,
		Email:           u.Email,
		Username:        u.Username,
		RequiredActions: u.RequiredActions,
		InvitedBy:       firstAttr(u.Attributes, "invited_by"),
		ExpiresAt:       firstAttr(u.Attributes, "expires_at"),
		CreatedAt:       u.toIdentity().CreatedAt,
		Status:          deriveInvitationStatus(u.Enabled, len(u.RequiredActions) > 0, firstAttr(u.Attributes, "expires_at")),
	}
	return &inv, nil
}

// compensateInvitationCreate best-effort deletes a half-provisioned user.
// The caller's ctx may be cancelled (often the reason we're cleaning up),
// so we mint a fresh background context with a short timeout.
//
// Failures are intentionally swallowed: we already have an error to return
// to the caller, and a failed cleanup leaves a recoverable state (the
// orphaned user can be re-deleted on retry).
func (p *Provider) compensateInvitationCreate(userID string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), compensatingDeleteTimeout)
	defer cancel()
	_ = p.client.doJSON(cleanupCtx, "DELETE", "/users/"+url.PathEscape(userID), nil, nil, nil)
}

// synthesizeFreshInvitation builds an Invitation from request fields when
// the trailing GET fails. The user IS provisioned (we got past step 4), so
// the email is already on its way; returning a synthesized response
// preserves work the caller can act on (the userID is durable and usable
// for follow-up calls like ResendInvitation / DeleteInvitation).
//
// CreatedAt is left zero — we have no authoritative timestamp without the
// GET, and faking time.Now() would mislead operators reading audit logs.
func (p *Provider) synthesizeFreshInvitation(userID string, req identity.CreateInvitationRequest) *identity.Invitation {
	return &identity.Invitation{
		ID:              userID,
		Email:           req.Email,
		Username:        req.Email,
		RequiredActions: append([]string{}, requiredActionsForInvite...),
		InvitedBy:       req.InvitedBy,
		ExpiresAt:       req.ExpiresAt,
		Status:          identity.InvitationStatusPending,
	}
}

// ─── Stage 5.2C — UPDATE (resend) ────────────────────────────────────────

// ResendInvitation re-dispatches the invitation action email, but ONLY for
// the actions the user still has pending — re-adding completed actions
// would force a user who already verified their email to verify again,
// corrupting their account state.
//
// Reliability guards:
//
//   - GET user first; if disabled, return ErrConflict (revoked invitations
//     can't be resurrected by resend — admin must re-enable first)
//   - if the user has no pending actions from the invite set, return
//     ErrConflict (accepted invitations have nothing to resend)
//   - PUT only the intersection of requiredActionsForInvite ∩ pending
//
// On GET failure after the PUT, we synthesize the response from the
// pre-PUT GET — same rationale as CreateInvitation's step 6.
func (p *Provider) ResendInvitation(ctx context.Context, userID string) (*identity.Invitation, error) {
	// 1. Inspect current state.
	var u kcUserWithActions
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(userID), nil, nil, &u); err != nil {
		return nil, err
	}

	if !u.Enabled {
		// Revoked: admin disabled the user. Resending an email won't
		// re-enable them; surface as conflict so the admin gets a clear
		// "this invite is in a terminal state, undo the revoke first"
		// signal from the API.
		return nil, fmt.Errorf("%w: invitation is revoked (user disabled)", identity.ErrConflict)
	}

	actions := intersectInviteActions(u.RequiredActions)
	if len(actions) == 0 {
		// Accepted: no invite actions remain. Includes the case where the
		// user has unrelated required actions (e.g. CONFIGURE_TOTP added
		// manually by an admin) — those aren't part of the invite flow
		// so we treat the invitation as accepted.
		return nil, fmt.Errorf("%w: invitation already accepted (no pending invite actions)", identity.ErrConflict)
	}

	// 2. Dispatch only the still-pending actions.
	if err := p.client.doJSON(ctx, "PUT", "/users/"+url.PathEscape(userID)+"/execute-actions-email", nil, actions, nil); err != nil {
		return nil, err
	}

	// 3. Refresh — required actions don't change as a side-effect of the
	//    PUT, but we re-GET so the response reflects any concurrent state
	//    change (e.g. user accepted between our two calls). If the
	//    refresh fails, fall back to the pre-PUT representation.
	var u2 kcUserWithActions
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(userID), nil, nil, &u2); err != nil {
		u2 = u
	}

	inv := identity.Invitation{
		ID:              u2.ID,
		Email:           u2.Email,
		Username:        u2.Username,
		RequiredActions: u2.RequiredActions,
		InvitedBy:       firstAttr(u2.Attributes, "invited_by"),
		ExpiresAt:       firstAttr(u2.Attributes, "expires_at"),
		CreatedAt:       u2.toIdentity().CreatedAt,
		Status:          deriveInvitationStatus(u2.Enabled, len(u2.RequiredActions) > 0, firstAttr(u2.Attributes, "expires_at")),
	}
	return &inv, nil
}

// intersectInviteActions returns the subset of `userActions` that the
// invitation flow cares about (VERIFY_EMAIL, UPDATE_PASSWORD). Other
// required actions (CONFIGURE_TOTP, terms of service, etc.) are NOT
// invite actions and are ignored here — resend is specifically about
// the invite email, not arbitrary action emails.
//
// Order is preserved from `userActions` so the PUT body matches the
// order Keycloak reported, which keeps tests deterministic.
func intersectInviteActions(userActions []string) []string {
	allowed := make(map[string]struct{}, len(requiredActionsForInvite))
	for _, a := range requiredActionsForInvite {
		allowed[a] = struct{}{}
	}
	out := make([]string, 0, len(userActions))
	for _, a := range userActions {
		if _, ok := allowed[a]; ok {
			out = append(out, a)
		}
	}
	return out
}
