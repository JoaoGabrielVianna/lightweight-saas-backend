package keycloak

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// Provider implements identity.IdentityProvider against a Keycloak realm
// via its Admin REST API.
type Provider struct {
	client *AdminClient
}

// NewProvider constructs a Provider on top of a configured AdminClient.
// Returns identity.ErrNotConfigured if any of BaseURL/Realm/ClientID/
// ClientSecret are missing — callers gate /users routes on this error.
func NewProvider(cfg AdminConfig) (*Provider, error) {
	c, err := NewAdminClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Provider{client: c}, nil
}

// kcUser mirrors the fields we consume from Keycloak's UserRepresentation.
// We intentionally don't bind the full schema — extra fields Keycloak adds
// in future versions just pass through to Attributes-like consumers later.
type kcUser struct {
	ID               string              `json:"id"`
	Username         string              `json:"username"`
	Email            string              `json:"email"`
	FirstName        string              `json:"firstName"`
	LastName         string              `json:"lastName"`
	Enabled          bool                `json:"enabled"`
	EmailVerified    bool                `json:"emailVerified"`
	CreatedTimestamp int64               `json:"createdTimestamp"` // ms since epoch
	Attributes       map[string][]string `json:"attributes,omitempty"`
}

func (u kcUser) toIdentity() identity.User {
	var created time.Time
	if u.CreatedTimestamp > 0 {
		created = time.UnixMilli(u.CreatedTimestamp).UTC()
	}
	return identity.User{
		ID:            u.ID,
		Username:      u.Username,
		Email:         u.Email,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Enabled:       u.Enabled,
		EmailVerified: u.EmailVerified,
		CreatedAt:     created,
		Attributes:    u.Attributes,
	}
}

// ListUsers calls GET /admin/realms/<realm>/users with the search/first/max
// query params Keycloak supports. The service layer is responsible for
// bounding Max — provider does not re-bound (would silently mask service bugs).
func (p *Provider) ListUsers(ctx context.Context, q identity.ListUsersQuery) ([]identity.User, error) {
	params := url.Values{}
	if q.Search != "" {
		params.Set("search", q.Search)
	}
	if q.First > 0 {
		params.Set("first", strconv.Itoa(q.First))
	}
	if q.Max > 0 {
		params.Set("max", strconv.Itoa(q.Max))
	}

	var raw []kcUser
	if err := p.client.doJSON(ctx, "GET", "/users", params, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]identity.User, 0, len(raw))
	for _, u := range raw {
		out = append(out, u.toIdentity())
	}
	return out, nil
}

// GetUser calls GET /admin/realms/<realm>/users/{id}. Maps Keycloak 404 to
// identity.ErrNotFound via the doJSON status switch.
func (p *Provider) GetUser(ctx context.Context, id string) (*identity.User, error) {
	var raw kcUser
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(id), nil, nil, &raw); err != nil {
		return nil, err
	}
	u := raw.toIdentity()
	return &u, nil
}
