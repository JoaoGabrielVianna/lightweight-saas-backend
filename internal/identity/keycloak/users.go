// Stage 5.2C/D — user mutations. Read paths live in provider.go alongside
// the kcUser shape; this file holds UPDATE + DELETE + reset-password.

package keycloak

import (
	"context"
	"net/url"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// kcUpdateUserBody is the PUT body for /users/:id. We send the full set of
// mutable fields verbatim (after the GET→merge step) so Keycloak's PUT
// semantics don't accidentally null-out a field the caller didn't touch.
type kcUpdateUserBody struct {
	Username      string              `json:"username"`
	Email         string              `json:"email"`
	FirstName     string              `json:"firstName"`
	LastName      string              `json:"lastName"`
	Enabled       bool                `json:"enabled"`
	EmailVerified bool                `json:"emailVerified"`
	Attributes    map[string][]string `json:"attributes,omitempty"`
}

// UpdateUser applies the partial update via GET→merge→PUT. We GET first so
// the PUT carries every field Keycloak expects; merging the request on top
// of the current shape lets callers send `{enabled:false}` without nulling
// firstName/lastName/email.
//
// The username field is treated as immutable here — Keycloak technically
// allows renaming but the realm config rejects it when registrationEmailAsUsername
// is true. v0.2 leaves usernames alone; rename support is a separate feature.
func (p *Provider) UpdateUser(ctx context.Context, id string, req identity.UpdateUserRequest) (*identity.User, error) {
	var current kcUser
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(id), nil, nil, &current); err != nil {
		return nil, err
	}

	body := kcUpdateUserBody{
		Username:      current.Username,
		Email:         current.Email,
		FirstName:     current.FirstName,
		LastName:      current.LastName,
		Enabled:       current.Enabled,
		EmailVerified: current.EmailVerified,
		Attributes:    current.Attributes,
	}
	if req.FirstName != nil {
		body.FirstName = *req.FirstName
	}
	if req.LastName != nil {
		body.LastName = *req.LastName
	}
	if req.Email != nil {
		body.Email = *req.Email
	}
	if req.Enabled != nil {
		body.Enabled = *req.Enabled
	}

	if err := p.client.doJSON(ctx, "PUT", "/users/"+url.PathEscape(id), nil, body, nil); err != nil {
		return nil, err
	}
	return p.GetUser(ctx, id)
}

// SendResetPasswordEmail queues an UPDATE_PASSWORD action for the user and
// asks Keycloak to dispatch the action email. Requires the realm to have
// SMTP configured — a 500 from Keycloak there surfaces as ErrAdminAPIUnavailable
// with a hint in the handler description.
func (p *Provider) SendResetPasswordEmail(ctx context.Context, userID string) error {
	actions := []string{"UPDATE_PASSWORD"}
	return p.client.doJSON(ctx, "PUT", "/users/"+url.PathEscape(userID)+"/execute-actions-email", nil, actions, nil)
}

// DeleteUser removes a user. Keycloak cascades role-mappings and sessions
// automatically; downstream effects (audit log entries, local DB rows in
// future iterations) are the service tier's responsibility.
func (p *Provider) DeleteUser(ctx context.Context, userID string) error {
	return p.client.doJSON(ctx, "DELETE", "/users/"+url.PathEscape(userID), nil, nil, nil)
}
