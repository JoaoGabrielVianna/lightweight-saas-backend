package keycloak

import (
	"context"
	"net/url"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// kcUserSession mirrors Keycloak's UserSessionRepresentation as returned
// by /users/:id/sessions and /clients/:cid/user-sessions.
type kcUserSession struct {
	ID         string            `json:"id"`
	UserID     string            `json:"userId"`
	Username   string            `json:"username"`
	IPAddress  string            `json:"ipAddress"`
	Start      int64             `json:"start"`      // ms since epoch
	LastAccess int64             `json:"lastAccess"` // ms since epoch
	Clients    map[string]string `json:"clients"`    // clientUUID → clientId
}

func (s kcUserSession) toIdentity() identity.Session {
	out := identity.Session{
		ID:        s.ID,
		UserID:    s.UserID,
		Username:  s.Username,
		IPAddress: s.IPAddress,
		Clients:   s.Clients,
	}
	if s.Start > 0 {
		out.StartedAt = time.UnixMilli(s.Start).UTC()
	}
	if s.LastAccess > 0 {
		out.LastAccess = time.UnixMilli(s.LastAccess).UTC()
	}
	return out
}

// ListUserSessions returns every active session for a user (across clients).
func (p *Provider) ListUserSessions(ctx context.Context, userID string) ([]identity.Session, error) {
	var raw []kcUserSession
	if err := p.client.doJSON(ctx, "GET", "/users/"+url.PathEscape(userID)+"/sessions", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]identity.Session, 0, len(raw))
	for _, s := range raw {
		out = append(out, s.toIdentity())
	}
	return out, nil
}

// kcClientBrief is the minimal client representation we need for aggregation.
type kcClientBrief struct {
	ID       string `json:"id"`       // Keycloak UUID
	ClientID string `json:"clientId"` // human-readable id (e.g. "saas-backend")
	Enabled  bool   `json:"enabled"`
}

// ListSessions returns active sessions across every enabled client in the
// realm. Keycloak has no realm-wide "all sessions" endpoint, so we
// aggregate /clients/:cid/user-sessions. Per-session deduplication is
// keyed on session id — a session that touches multiple clients yields
// one entry whose Clients map carries every client name that's been seen.
//
// Cost: one round-trip per enabled client. At typical realm size (<20
// clients) this is acceptable. Optimize when needed.
func (p *Provider) ListSessions(ctx context.Context) ([]identity.Session, error) {
	var clients []kcClientBrief
	if err := p.client.doJSON(ctx, "GET", "/clients", nil, nil, &clients); err != nil {
		return nil, err
	}

	seen := make(map[string]*identity.Session)
	for _, c := range clients {
		if !c.Enabled {
			continue
		}
		var sessions []kcUserSession
		if err := p.client.doJSON(ctx, "GET", "/clients/"+url.PathEscape(c.ID)+"/user-sessions", nil, nil, &sessions); err != nil {
			// One client failing shouldn't poison the whole listing —
			// continue and let the caller see what we could collect.
			continue
		}
		for _, s := range sessions {
			if existing, ok := seen[s.ID]; ok {
				// Same session touched another client; merge into the
				// Clients map. Use the Keycloak client uuid as the
				// dedup key.
				if existing.Clients == nil {
					existing.Clients = map[string]string{}
				}
				existing.Clients[c.ID] = c.ClientID
				continue
			}
			projected := s.toIdentity()
			if projected.Clients == nil {
				projected.Clients = map[string]string{}
			}
			projected.Clients[c.ID] = c.ClientID
			seen[s.ID] = &projected
		}
	}

	out := make([]identity.Session, 0, len(seen))
	for _, s := range seen {
		out = append(out, *s)
	}
	return out, nil
}

// ─── Stage 5.2D — DELETE ──────────────────────────────────────────────────

// DeleteSession revokes a single Keycloak session by id. Returns 404 when
// the session is already gone (already-revoked or expired). We surface that
// as identity.ErrNotFound — the caller can choose whether it's an error or
// a no-op for their flow.
func (p *Provider) DeleteSession(ctx context.Context, sessionID string) error {
	return p.client.doJSON(ctx, "DELETE", "/sessions/"+url.PathEscape(sessionID), nil, nil, nil)
}

// LogoutUserSessions revokes every active session for a user across every
// client. Keycloak exposes this as POST /users/:id/logout. The endpoint
// returns 204 on success even if the user had no live sessions.
func (p *Provider) LogoutUserSessions(ctx context.Context, userID string) error {
	return p.client.doJSON(ctx, "POST", "/users/"+url.PathEscape(userID)+"/logout", nil, nil, nil)
}
