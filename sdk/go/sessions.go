package lightweight

import (
	"context"
	"net/http"
)

// SessionsService reads and revokes active sessions in the client's workspace.
//
// Method names here are chosen to state their BLAST RADIUS, because the
// difference between signing out one browser and signing out one person is
// invisible in a URL and very visible to whoever is signed out. There is no
// method that signs out the whole workspace, because the API has no such
// operation; if that ever changes, the method should be named so that nobody
// calls it by mistake.
//
// Obtain it from [Client.Sessions].
type SessionsService struct {
	client *Client
}

// sessionListEnvelope is the wire shape of the session collections. Unwrapped
// for the same reason as roleListEnvelope: its `count` is len(sessions).
type sessionListEnvelope struct {
	Sessions []Session `json:"sessions"`
}

// List returns every active session in the workspace, for all users.
//
// Read-only, complete and unpaginated. Useful for an operator dashboard; be
// aware that on a large workspace this is a large response.
//
// Required scope: sessions:read.
func (s *SessionsService) List(ctx context.Context) ([]Session, error) {
	const op = "Sessions.List"
	path := s.client.workspacePath("sessions")

	var out sessionListEnvelope
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// ListForUser returns one user's active sessions.
//
// Required scope: sessions:read.
func (s *SessionsService) ListForUser(ctx context.Context, userID string) ([]Session, error) {
	const op = "Sessions.ListForUser"
	if err := requiredArg(op, "userID", userID); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("users", userID, "sessions")

	var out sessionListEnvelope
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// Revoke ends ONE session.
//
// Blast radius: a single sign-in on a single device. The user's other sessions
// are untouched and they can sign in again immediately. This is the right call
// for "sign this device out".
//
// Required scope: sessions:revoke.
func (s *SessionsService) Revoke(ctx context.Context, sessionID string) error {
	const op = "Sessions.Revoke"
	if err := requiredArg(op, "sessionID", sessionID); err != nil {
		return err
	}
	path := s.client.workspacePath("sessions", sessionID)
	return s.client.do(ctx, op, http.MethodDelete, path, nil, nil, nil)
}

// RevokeAllForUser ends EVERY session belonging to one user.
//
// Blast radius: that person is signed out everywhere, on every device, at once.
// It does not disable the account — they can sign in again — so it is the right
// call after a suspected credential compromise, paired with
// [UsersService.SendPasswordReset], and the wrong call for routine cleanup.
//
// The verbose name is the point: a caller reaching for this should have to
// notice that it is not [SessionsService.Revoke].
//
// Required scope: sessions:revoke.
func (s *SessionsService) RevokeAllForUser(ctx context.Context, userID string) error {
	const op = "Sessions.RevokeAllForUser"
	if err := requiredArg(op, "userID", userID); err != nil {
		return err
	}
	path := s.client.workspacePath("users", userID, "sessions")
	return s.client.do(ctx, op, http.MethodDelete, path, nil, nil, nil)
}
