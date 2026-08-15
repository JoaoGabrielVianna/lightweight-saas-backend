package lightweight

import (
	"context"
	"net/http"
	"time"
)

// InvitationsService administers pending invitations in the client's workspace.
//
// An invitation is the alternative to [UsersService.Create]: instead of choosing
// a credential for someone and having to transport it to them, the workspace
// emails them and they choose their own. Prefer it whenever the workspace can
// send mail — a password your service never sees is a password your service
// cannot leak.
//
// Obtain it from [Client.Invitations].
type InvitationsService struct {
	client *Client
}

// invitationListEnvelope is the wire shape of the invitation collection.
type invitationListEnvelope struct {
	Invitations []Invitation `json:"invitations"`
}

// List returns the workspace's pending invitations.
//
// Complete and unpaginated. Accepted invitations are not listed: once accepted,
// the record is an ordinary user and appears in [UsersService.List] instead.
//
// Required scope: invitations:read.
func (s *InvitationsService) List(ctx context.Context) ([]Invitation, error) {
	const op = "Invitations.List"
	path := s.client.workspacePath("invitations")

	var out invitationListEnvelope
	if err := s.client.do(ctx, op, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Invitations, nil
}

// CreateInvitationRequest is the body of [InvitationsService.Create].
type CreateInvitationRequest struct {
	// Email is required and is where the invitation is sent.
	Email string `json:"email"`

	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`

	// Roles is REQUIRED and must name at least one existing role.
	//
	// Unlike [CreateUserRequest], where roles can be granted afterwards, an
	// invitation with no roles invites someone to an account that can do
	// nothing — so the server refuses it rather than creating a puzzle for
	// whoever accepts.
	Roles []string `json:"roles"`

	// ExpiresAt is an optional expiry, which must be in the future. nil means
	// the invitation does not expire on its own.
	//
	// A *time.Time rather than a string on this side, because here the value is
	// being WRITTEN and this package controls the format. It is serialised as
	// RFC 3339 in UTC. Reading it back is a different matter — see
	// [Invitation.ExpiresAt].
	ExpiresAt *time.Time `json:"-"`

	// InvitedBy records who sent the invitation. Optional; the server attributes
	// it to the authenticated caller when omitted.
	InvitedBy string `json:"invited_by,omitempty"`
}

// wireCreateInvitation is the JSON body actually sent.
//
// A separate type because [CreateInvitationRequest.ExpiresAt] is a *time.Time
// for the caller and an RFC 3339 string on the wire, and a MarshalJSON on the
// public type would have to be kept in step with every field added to it. This
// way the mapping is one visible function.
type wireCreateInvitation struct {
	Email     string   `json:"email"`
	FirstName string   `json:"first_name,omitempty"`
	LastName  string   `json:"last_name,omitempty"`
	Roles     []string `json:"roles"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	InvitedBy string   `json:"invited_by,omitempty"`
}

func (r CreateInvitationRequest) wire() wireCreateInvitation {
	out := wireCreateInvitation{
		Email:     r.Email,
		FirstName: r.FirstName,
		LastName:  r.LastName,
		Roles:     r.Roles,
		InvitedBy: r.InvitedBy,
	}
	if r.ExpiresAt != nil {
		out.ExpiresAt = r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}

// Create sends an invitation and returns it.
//
// The invited person receives an email and completes sign-up themselves; no
// password passes through your service. An address that already belongs to a
// user is refused with [CodeConflict].
//
// This needs the workspace to be able to send mail. A workspace whose provider
// has no working mail configuration fails here rather than creating a user who
// will never hear about it.
//
// Required scope: invitations:write.
func (s *InvitationsService) Create(ctx context.Context, req CreateInvitationRequest) (*Invitation, error) {
	const op = "Invitations.Create"
	path := s.client.workspacePath("invitations")

	var out Invitation
	if err := s.client.do(ctx, op, http.MethodPost, path, nil, req.wire(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Resend sends the invitation email again and returns the invitation.
//
// The previous email's link is not invalidated by this. An invitation with
// nothing left to send — one that has already been accepted — is refused with
// [CodeConflict].
//
// Required scope: invitations:write.
func (s *InvitationsService) Resend(ctx context.Context, invitationID string) (*Invitation, error) {
	const op = "Invitations.Resend"
	if err := requiredArg(op, "invitationID", invitationID); err != nil {
		return nil, err
	}
	path := s.client.workspacePath("invitations", invitationID, "resend")

	var out Invitation
	if err := s.client.do(ctx, op, http.MethodPost, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Revoke withdraws a pending invitation, WHICH DELETES THE INVITED USER.
//
// This is not a cancellation of a message. An invitation IS a user record in an
// incomplete state, so withdrawing it removes that record: the address becomes
// free to invite again, and any link already emailed stops working.
//
// It is safe precisely because the account was never completed. It is NOT a way
// to remove someone who has accepted — the server refuses that with
// [CodeInvitationNotFound], because by then the record is an ordinary user and
// [UsersService.Delete] is the operation that applies.
//
// Required scope: invitations:write.
func (s *InvitationsService) Revoke(ctx context.Context, invitationID string) error {
	const op = "Invitations.Revoke"
	if err := requiredArg(op, "invitationID", invitationID); err != nil {
		return err
	}
	path := s.client.workspacePath("invitations", invitationID)
	return s.client.do(ctx, op, http.MethodDelete, path, nil, nil, nil)
}
