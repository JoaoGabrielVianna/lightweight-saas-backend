package keycloak

import (
	"context"
	"net/url"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// SMTPConfig mirrors Keycloak's smtpServer realm property exactly.
type SMTPConfig struct {
	Host            string `json:"host"`
	Port            string `json:"port"`
	From            string `json:"from"`
	FromDisplayName string `json:"fromDisplayName"`
	SSL             string `json:"ssl"`
	StartTLS        string `json:"starttls"`
	Auth            string `json:"auth"`
	User            string `json:"user"`
	Password        string `json:"password"`
	ReplyTo         string `json:"replyTo"`
}

// CreateUserWithPasswordRequest is the input for provisioning a user with a
// temporary password instead of the invite-by-email flow.
type CreateUserWithPasswordRequest struct {
	Email             string
	FirstName         string
	LastName          string
	TemporaryPassword string
	Roles             []string
}

// GetSMTPConfig fetches the realm's smtpServer block from Keycloak.
func (p *Provider) GetSMTPConfig(ctx context.Context) (*SMTPConfig, error) {
	var realm struct {
		SMTPServer SMTPConfig `json:"smtpServer"`
	}
	if err := p.client.doJSON(ctx, "GET", "", nil, nil, &realm); err != nil {
		return nil, err
	}
	return &realm.SMTPServer, nil
}

// UpdateSMTPConfig replaces the realm's smtpServer block in Keycloak.
// Keycloak's PUT /admin/realms/{realm} applies a partial update for fields
// present in the request body, leaving everything else untouched.
func (p *Provider) UpdateSMTPConfig(ctx context.Context, cfg SMTPConfig) error {
	body := map[string]any{"smtpServer": cfg}
	return p.client.doJSON(ctx, "PUT", "", nil, body, nil)
}

// CreateUserWithPassword provisions a Keycloak user with a temporary
// password. On first login the user must change the password (Keycloak
// enforces this automatically when temporary=true).
//
// Reliability: if role assignment fails after the user was created, the
// user is best-effort deleted so the caller can retry without a 409.
func (p *Provider) CreateUserWithPassword(ctx context.Context, req CreateUserWithPasswordRequest) (*identity.User, error) {
	userID, err := p.client.doCreate(ctx, "/users", map[string]any{
		"username":        req.Email,
		"email":           req.Email,
		"firstName":       req.FirstName,
		"lastName":        req.LastName,
		"enabled":         true,
		"emailVerified":   false,
		"requiredActions": []string{"UPDATE_PASSWORD"},
	})
	if err != nil {
		return nil, err
	}

	if err := p.client.doJSON(ctx, "PUT", "/users/"+url.PathEscape(userID)+"/reset-password", nil, map[string]any{
		"type":      "password",
		"value":     req.TemporaryPassword,
		"temporary": true,
	}, nil); err != nil {
		_ = p.client.doJSON(ctx, "DELETE", "/users/"+url.PathEscape(userID), nil, nil, nil)
		return nil, err
	}

	if len(req.Roles) > 0 {
		if err := p.AssignRolesToUser(ctx, userID, req.Roles); err != nil {
			_ = p.client.doJSON(ctx, "DELETE", "/users/"+url.PathEscape(userID), nil, nil, nil)
			return nil, err
		}
	}

	var raw kcUser
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(userID), nil, nil, &raw); err != nil {
		return &identity.User{ID: userID, Email: req.Email, Username: req.Email, Enabled: true}, nil
	}
	u := raw.toIdentity()
	return &u, nil
}
