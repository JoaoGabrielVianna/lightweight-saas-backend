package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeProvider satisfies IdentityProvider in-memory. Tests reach in to
// override behavior per case. Defaults: every method returns empty result.
type fakeProvider struct {
	lastQuery ListUsersQuery
	users     []User
	getErr    error

	// Stage A captures
	lastRoleName   string
	lastUserIDArg  string
	rolesReturn    []Role
	roleReturn     *Role
	sessionsReturn []Session
	invitesReturn  []Invitation
	usersByRoleArg string

	// Stage B captures + behaviour hooks (see createCalls type below)
	createCalls createCalls

	// Stage C/D captures + behaviour hooks (see mutationCalls type below)
	mutationCalls mutationCalls
}

func (f *fakeProvider) ListUsers(_ context.Context, q ListUsersQuery) ([]User, error) {
	f.lastQuery = q
	return f.users, nil
}
func (f *fakeProvider) GetUser(_ context.Context, id string) (*User, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &User{ID: id}, nil
}
func (f *fakeProvider) ListRoles(_ context.Context) ([]Role, error) {
	return f.rolesReturn, nil
}
func (f *fakeProvider) GetRole(_ context.Context, name string) (*Role, error) {
	f.lastRoleName = name
	if f.roleReturn != nil {
		return f.roleReturn, nil
	}
	return &Role{Name: name}, nil
}
func (f *fakeProvider) ListUsersByRole(_ context.Context, name string) ([]User, error) {
	f.usersByRoleArg = name
	// When tests stage a specific admin set via mutationCalls.adminsForLastCheck,
	// honor it for the "admin" role lookup so the last-admin guard can be
	// exercised in isolation.
	if name == adminRoleName && f.mutationCalls.adminsForLastCheck != nil {
		return f.mutationCalls.adminsForLastCheck, nil
	}
	return f.users, nil
}
func (f *fakeProvider) ListUserRoles(_ context.Context, userID string) ([]Role, error) {
	f.lastUserIDArg = userID
	return f.rolesReturn, nil
}
func (f *fakeProvider) ListUserSessions(_ context.Context, userID string) ([]Session, error) {
	f.lastUserIDArg = userID
	return f.sessionsReturn, nil
}
func (f *fakeProvider) ListSessions(_ context.Context) ([]Session, error) {
	return f.sessionsReturn, nil
}
func (f *fakeProvider) ListInvitations(_ context.Context) ([]Invitation, error) {
	return f.invitesReturn, nil
}

// Stage B captures + behaviour hooks
type createCalls struct {
	createRoleReq          *CreateRoleRequest
	createInvitationReq    *CreateInvitationRequest
	createRoleErr          error
	createInvitationErr    error
	createRoleReturn       *Role
	createInvitationReturn *Invitation
}

func (f *fakeProvider) CreateRole(_ context.Context, req CreateRoleRequest) (*Role, error) {
	f.createCalls.createRoleReq = &req
	if f.createCalls.createRoleErr != nil {
		return nil, f.createCalls.createRoleErr
	}
	if f.createCalls.createRoleReturn != nil {
		return f.createCalls.createRoleReturn, nil
	}
	return &Role{Name: req.Name, Description: req.Description}, nil
}

func (f *fakeProvider) CreateInvitation(_ context.Context, req CreateInvitationRequest) (*Invitation, error) {
	f.createCalls.createInvitationReq = &req
	if f.createCalls.createInvitationErr != nil {
		return nil, f.createCalls.createInvitationErr
	}
	if f.createCalls.createInvitationReturn != nil {
		return f.createCalls.createInvitationReturn, nil
	}
	return &Invitation{ID: "u-new", Email: req.Email, Status: "pending", InvitedBy: req.InvitedBy, ExpiresAt: req.ExpiresAt}, nil
}

// ─── Stage C/D fake-provider plumbing ────────────────────────────────────

// mutationCalls captures every UPDATE/DELETE call so tests can assert the
// service forwarded the right arguments AFTER applying guards/normalization.
type mutationCalls struct {
	updateUserID        string
	updateUserReq       UpdateUserRequest
	updateRoleName      string
	updateRoleReq       UpdateRoleRequest
	assignUserID        string
	assignRoles         []string
	unassignUserID      string
	unassignRoles       []string
	resetPasswordUserID string
	resendInvitationID  string
	deleteUserID        string
	deleteRoleName      string
	deleteSessionID     string
	logoutUserID        string

	// Optional injected errors (per method).
	updateUserErr       error
	updateRoleErr       error
	assignErr           error
	unassignErr         error
	resetPasswordErr    error
	resendInvitationErr error
	deleteUserErr       error
	deleteRoleErr       error
	deleteSessionErr    error
	logoutErr           error

	// Optional injected admin set for assertNotLastAdmin checks. When nil,
	// ListUsersByRole("admin") returns whatever `users` holds.
	adminsForLastCheck []User
}

func (f *fakeProvider) UpdateUser(_ context.Context, id string, req UpdateUserRequest) (*User, error) {
	f.mutationCalls.updateUserID = id
	f.mutationCalls.updateUserReq = req
	if f.mutationCalls.updateUserErr != nil {
		return nil, f.mutationCalls.updateUserErr
	}
	u := &User{ID: id, Enabled: true}
	if req.Enabled != nil {
		u.Enabled = *req.Enabled
	}
	return u, nil
}

func (f *fakeProvider) UpdateRole(_ context.Context, name string, req UpdateRoleRequest) (*Role, error) {
	f.mutationCalls.updateRoleName = name
	f.mutationCalls.updateRoleReq = req
	if f.mutationCalls.updateRoleErr != nil {
		return nil, f.mutationCalls.updateRoleErr
	}
	r := &Role{Name: name}
	if req.Description != nil {
		r.Description = *req.Description
	}
	return r, nil
}

func (f *fakeProvider) AssignRolesToUser(_ context.Context, userID string, roles []string) error {
	f.mutationCalls.assignUserID = userID
	f.mutationCalls.assignRoles = roles
	return f.mutationCalls.assignErr
}

func (f *fakeProvider) UnassignRolesFromUser(_ context.Context, userID string, roles []string) error {
	f.mutationCalls.unassignUserID = userID
	f.mutationCalls.unassignRoles = roles
	return f.mutationCalls.unassignErr
}

func (f *fakeProvider) SendResetPasswordEmail(_ context.Context, userID string) error {
	f.mutationCalls.resetPasswordUserID = userID
	return f.mutationCalls.resetPasswordErr
}

func (f *fakeProvider) SetUserPassword(_ context.Context, userID, _ string, _ bool) error {
	f.mutationCalls.resetPasswordUserID = userID
	return f.mutationCalls.resetPasswordErr
}

func (f *fakeProvider) ResendInvitation(_ context.Context, userID string) (*Invitation, error) {
	f.mutationCalls.resendInvitationID = userID
	if f.mutationCalls.resendInvitationErr != nil {
		return nil, f.mutationCalls.resendInvitationErr
	}
	return &Invitation{ID: userID, Status: "pending"}, nil
}

func (f *fakeProvider) DeleteUser(_ context.Context, userID string) error {
	f.mutationCalls.deleteUserID = userID
	return f.mutationCalls.deleteUserErr
}

func (f *fakeProvider) DeleteRole(_ context.Context, name string) error {
	f.mutationCalls.deleteRoleName = name
	return f.mutationCalls.deleteRoleErr
}

func (f *fakeProvider) DeleteSession(_ context.Context, sessionID string) error {
	f.mutationCalls.deleteSessionID = sessionID
	return f.mutationCalls.deleteSessionErr
}

func (f *fakeProvider) LogoutUserSessions(_ context.Context, userID string) error {
	f.mutationCalls.logoutUserID = userID
	return f.mutationCalls.logoutErr
}

func TestService_ListUsers_AppliesDefaultPageSize(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	_, err := s.ListUsers(context.Background(), ListUsersQuery{})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if fp.lastQuery.Max != defaultPageSize {
		t.Errorf("expected default page size %d, got %d", defaultPageSize, fp.lastQuery.Max)
	}
}

func TestService_ListUsers_ClampsMaxPageSize(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	_, err := s.ListUsers(context.Background(), ListUsersQuery{Max: 9999})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if fp.lastQuery.Max != maxPageSize {
		t.Errorf("expected max %d, got %d", maxPageSize, fp.lastQuery.Max)
	}
}

func TestService_ListUsers_NormalizesNegativeFirst(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	_, err := s.ListUsers(context.Background(), ListUsersQuery{First: -50, Max: 10})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if fp.lastQuery.First != 0 {
		t.Errorf("expected First=0, got %d", fp.lastQuery.First)
	}
	if fp.lastQuery.Max != 10 {
		t.Errorf("expected explicit Max=10 preserved, got %d", fp.lastQuery.Max)
	}
}

func TestService_GetUser_RejectsNonUUID(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	cases := []string{"", "123", "not-a-uuid", "../../../etc/passwd", "abc"}
	for _, id := range cases {
		_, err := s.GetUser(context.Background(), id)
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("id=%q: expected ErrBadRequest, got %v", id, err)
		}
	}
}

func TestService_GetUser_AcceptsValidUUID(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	const id = "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc"
	u, err := s.GetUser(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.ID != id {
		t.Errorf("got id=%q", u.ID)
	}
}

func TestService_GetUser_PropagatesProviderErrors(t *testing.T) {
	fp := &fakeProvider{getErr: ErrNotFound}
	s := NewService(fp)

	_, err := s.GetUser(context.Background(), "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound propagated, got %v", err)
	}
}

func TestNewService_NilProvider_ReturnsNil(t *testing.T) {
	if got := NewService(nil); got != nil {
		t.Errorf("expected nil service when provider is nil, got %+v", got)
	}
}

// ─────────────── Stage A: read methods ───────────────

func TestService_GetRole_RejectsMalformedName(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	bad := []string{"", "../passwd", "name with\ttab", "role!!", "   "}
	for _, name := range bad {
		_, err := s.GetRole(context.Background(), name)
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("name=%q: expected ErrBadRequest, got %v", name, err)
		}
	}
	if fp.lastRoleName != "" {
		t.Errorf("malformed names should never reach the provider, got %q", fp.lastRoleName)
	}
}

func TestService_GetRole_AcceptsCommonNames(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	cases := []string{"admin", "user", "default-roles-saas", "team lead", "offline_access", "scope:read"}
	for _, name := range cases {
		if _, err := s.GetRole(context.Background(), name); err != nil {
			t.Errorf("name=%q should pass, got %v", name, err)
		}
	}
}

func TestService_ListUserRoles_ValidatesUUID(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	_, err := s.ListUserRoles(context.Background(), "not-a-uuid")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for non-UUID, got %v", err)
	}
	if _, err := s.ListUserRoles(context.Background(), "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc"); err != nil {
		t.Errorf("valid UUID should pass, got %v", err)
	}
}

func TestService_ListUserSessions_ValidatesUUID(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	if _, err := s.ListUserSessions(context.Background(), "bogus"); !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestService_ListRoles_ProxiesProviderResult(t *testing.T) {
	fp := &fakeProvider{rolesReturn: []Role{{Name: "admin"}, {Name: "user"}}}
	s := NewService(fp)
	roles, err := s.ListRoles(context.Background())
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(roles))
	}
}

func TestService_ListSessions_ProxiesProviderResult(t *testing.T) {
	fp := &fakeProvider{sessionsReturn: []Session{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	s := NewService(fp)
	sessions, err := s.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}
}

func TestService_ListInvitations_ProxiesProviderResult(t *testing.T) {
	fp := &fakeProvider{invitesReturn: []Invitation{{ID: "u1", Status: "pending"}}}
	s := NewService(fp)
	invs, err := s.ListInvitations(context.Background())
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invs) != 1 || invs[0].Status != "pending" {
		t.Errorf("invitation passthrough lost: %+v", invs)
	}
}

// ─────────────── Stage 5.2B: CreateRole ───────────────

func TestCreateRole_Success_NormalizesName(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	role, err := s.CreateRole(context.Background(), CreateRoleRequest{Name: "  SUPPORT  ", Description: " Support team "})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if role.Name != "support" {
		t.Errorf("expected normalized name 'support', got %q", role.Name)
	}
	// Provider should have received the normalized form.
	if fp.createCalls.createRoleReq.Name != "support" {
		t.Errorf("provider received non-normalized name: %q", fp.createCalls.createRoleReq.Name)
	}
	if fp.createCalls.createRoleReq.Description != "Support team" {
		t.Errorf("description not trimmed: %q", fp.createCalls.createRoleReq.Description)
	}
}

func TestCreateRole_RejectsEmpty(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	for _, name := range []string{"", "   ", "\t\n "} {
		_, err := s.CreateRole(context.Background(), CreateRoleRequest{Name: name})
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("name=%q: expected ErrBadRequest, got %v", name, err)
		}
	}
	if fp.createCalls.createRoleReq != nil {
		t.Errorf("empty-name request should never reach provider")
	}
}

func TestCreateRole_RejectsBuiltinNames(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	// Test against names AND mixed-case variants — normalization runs first.
	cases := []string{"admin", "user", "Admin", "USER", "offline_access", "uma_authorization", "default-roles-saas", "default-roles-anything"}
	for _, name := range cases {
		_, err := s.CreateRole(context.Background(), CreateRoleRequest{Name: name})
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("name=%q: expected ErrBadRequest, got %v", name, err)
		}
	}
}

func TestCreateRole_RejectsTooLong(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	long := make([]byte, maxRoleNameLen+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := s.CreateRole(context.Background(), CreateRoleRequest{Name: string(long)})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for >max name, got %v", err)
	}
}

func TestCreateRole_RejectsInvalidCharacters(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	bad := []string{"role with space", "role!bang", "role/slash", "*", "中文"}
	for _, name := range bad {
		_, err := s.CreateRole(context.Background(), CreateRoleRequest{Name: name})
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("name=%q: expected ErrBadRequest, got %v", name, err)
		}
	}
}

func TestCreateRole_PropagatesProviderConflict(t *testing.T) {
	fp := &fakeProvider{createCalls: createCalls{createRoleErr: ErrConflict}}
	s := NewService(fp)
	_, err := s.CreateRole(context.Background(), CreateRoleRequest{Name: "support"})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

// ─────────────── Stage 5.2B: CreateInvitation ───────────────

func TestCreateInvitation_Success_DefaultsInvitedByFromIdentity(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	inv, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{
		Email: "jane@example.com",
		Roles: []string{"user"},
	}, "adminuser@example.com")
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if fp.createCalls.createInvitationReq.InvitedBy != "adminuser@example.com" {
		t.Errorf("invited_by should default from identity, got %q", fp.createCalls.createInvitationReq.InvitedBy)
	}
	if inv.Email != "jane@example.com" {
		t.Errorf("email passthrough wrong: %q", inv.Email)
	}
}

func TestCreateInvitation_ExplicitInvitedByWins(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)

	_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{
		Email:     "jane@example.com",
		Roles:     []string{"user"},
		InvitedBy: "ci-pipeline",
	}, "adminuser@example.com")
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if fp.createCalls.createInvitationReq.InvitedBy != "ci-pipeline" {
		t.Errorf("explicit invited_by should win over default, got %q", fp.createCalls.createInvitationReq.InvitedBy)
	}
}

func TestCreateInvitation_RejectsEmptyEmail(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{Email: "", Roles: []string{"user"}}, "")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for empty email, got %v", err)
	}
}

func TestCreateInvitation_RejectsMalformedEmail(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	bad := []string{"not-an-email", "@x.test", "x@", "x@@y.test", "no spaces @x.test"}
	for _, e := range bad {
		_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{Email: e, Roles: []string{"user"}}, "")
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("email=%q: expected ErrBadRequest, got %v", e, err)
		}
	}
}

func TestCreateInvitation_RejectsEmptyRoles(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{Email: "j@x.test", Roles: []string{}}, "")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for empty roles, got %v", err)
	}
	// Also reject when any role entry is empty/whitespace.
	_, err = s.CreateInvitation(context.Background(), CreateInvitationRequest{Email: "j@x.test", Roles: []string{"user", "   "}}, "")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for whitespace-only role entry, got %v", err)
	}
}

func TestCreateInvitation_RejectsPastExpiresAt(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	pastISO := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{
		Email:     "j@x.test",
		Roles:     []string{"user"},
		ExpiresAt: pastISO,
	}, "")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for past expires_at, got %v", err)
	}
}

func TestCreateInvitation_RejectsUnparseableExpiresAt(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{
		Email:     "j@x.test",
		Roles:     []string{"user"},
		ExpiresAt: "tomorrow",
	}, "")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for unparseable expires_at, got %v", err)
	}
}

func TestCreateInvitation_NormalizesExpiresAtToRFC3339UTC(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	futureISO := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{
		Email:     "j@x.test",
		Roles:     []string{"user"},
		ExpiresAt: futureISO,
	}, "")
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	// Service should have re-formatted the value into canonical RFC3339.
	if fp.createCalls.createInvitationReq.ExpiresAt == "" {
		t.Errorf("expires_at lost on the way to provider")
	}
	if _, err := time.Parse(time.RFC3339, fp.createCalls.createInvitationReq.ExpiresAt); err != nil {
		t.Errorf("expires_at not in RFC3339 form: %q", fp.createCalls.createInvitationReq.ExpiresAt)
	}
}

func TestCreateInvitation_NormalizesEmailLowercase(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{
		Email: "  JANE@Example.COM  ",
		Roles: []string{"USER"},
	}, "")
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if fp.createCalls.createInvitationReq.Email != "jane@example.com" {
		t.Errorf("email not lowercased+trimmed: %q", fp.createCalls.createInvitationReq.Email)
	}
	if fp.createCalls.createInvitationReq.Roles[0] != "user" {
		t.Errorf("role not lowercased: %q", fp.createCalls.createInvitationReq.Roles[0])
	}
}

func TestCreateInvitation_PropagatesProviderErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"duplicate email", ErrConflict},
		{"missing role", ErrNotFound},
		{"upstream down", ErrAdminAPIUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakeProvider{createCalls: createCalls{createInvitationErr: tc.err}}
			s := NewService(fp)
			_, err := s.CreateInvitation(context.Background(), CreateInvitationRequest{
				Email: "j@x.test",
				Roles: []string{"user"},
			}, "adminuser")
			if !errors.Is(err, tc.err) {
				t.Errorf("expected %v, got %v", tc.err, err)
			}
		})
	}
}

// ─────────────── Stage 5.2C — UpdateUser ───────────────

const (
	callerUUID = "11111111-1111-1111-1111-111111111111"
	otherUUID  = "22222222-2222-2222-2222-222222222222"
	thirdUUID  = "33333333-3333-3333-3333-333333333333"
)

func TestUpdateUser_RejectsNonUUID(t *testing.T) {
	s := NewService(&fakeProvider{})
	_, err := s.UpdateUser(context.Background(), callerUUID, "not-a-uuid", UpdateUserRequest{})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestUpdateUser_RejectsMalformedEmail(t *testing.T) {
	s := NewService(&fakeProvider{})
	bad := "no-at-sign"
	_, err := s.UpdateUser(context.Background(), callerUUID, otherUUID, UpdateUserRequest{Email: &bad})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestUpdateUser_NormalizesEmailLowercase(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	mixed := "  Jane@Example.COM  "
	_, err := s.UpdateUser(context.Background(), callerUUID, otherUUID, UpdateUserRequest{Email: &mixed})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if got := *fp.mutationCalls.updateUserReq.Email; got != "jane@example.com" {
		t.Errorf("email not normalized: %q", got)
	}
}

func TestUpdateUser_RejectsSelfDisable(t *testing.T) {
	s := NewService(&fakeProvider{})
	f := false
	_, err := s.UpdateUser(context.Background(), callerUUID, callerUUID, UpdateUserRequest{Enabled: &f})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for self-disable, got %v", err)
	}
}

func TestUpdateUser_RejectsDisablingLastAdmin(t *testing.T) {
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{{ID: otherUUID, Enabled: true}}
	s := NewService(fp)
	f := false
	_, err := s.UpdateUser(context.Background(), callerUUID, otherUUID, UpdateUserRequest{Enabled: &f})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for last-admin disable, got %v", err)
	}
}

func TestUpdateUser_DisablingNonLastAdminAllowed(t *testing.T) {
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{
		{ID: otherUUID, Enabled: true},
		{ID: thirdUUID, Enabled: true},
	}
	s := NewService(fp)
	f := false
	_, err := s.UpdateUser(context.Background(), callerUUID, otherUUID, UpdateUserRequest{Enabled: &f})
	if err != nil {
		t.Errorf("disabling one of two admins should be allowed, got %v", err)
	}
}

// ─────────────── Stage 5.2C — UpdateRole ───────────────

func TestUpdateRole_RejectsProtectedNames(t *testing.T) {
	s := NewService(&fakeProvider{})
	cases := []string{"admin", "user", "default-roles-saas", "offline_access", "uma_authorization"}
	for _, name := range cases {
		_, err := s.UpdateRole(context.Background(), name, UpdateRoleRequest{})
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("name=%q: expected ErrForbidden, got %v", name, err)
		}
	}
}

func TestUpdateRole_AllowsUserManagedRoles(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	desc := "new description"
	_, err := s.UpdateRole(context.Background(), "editor", UpdateRoleRequest{Description: &desc})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if fp.mutationCalls.updateRoleName != "editor" {
		t.Errorf("provider received wrong name: %q", fp.mutationCalls.updateRoleName)
	}
}

// ─────────────── Stage 5.2C — Assign / Unassign roles ───────────────

func TestAssignRolesToUser_RejectsNonUUID(t *testing.T) {
	s := NewService(&fakeProvider{})
	err := s.AssignRolesToUser(context.Background(), "bogus", []string{"editor"})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestAssignRolesToUser_RejectsEmptyList(t *testing.T) {
	s := NewService(&fakeProvider{})
	err := s.AssignRolesToUser(context.Background(), otherUUID, []string{})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestAssignRolesToUser_NormalizesRoleNames(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	if err := s.AssignRolesToUser(context.Background(), otherUUID, []string{"  Editor  ", "VIEWER"}); err != nil {
		t.Fatalf("AssignRolesToUser: %v", err)
	}
	if got := fp.mutationCalls.assignRoles; len(got) != 2 || got[0] != "editor" || got[1] != "viewer" {
		t.Errorf("roles not normalized: %v", got)
	}
}

func TestUnassignRolesFromUser_RejectsSelfStripAdmin(t *testing.T) {
	s := NewService(&fakeProvider{})
	err := s.UnassignRolesFromUser(context.Background(), callerUUID, callerUUID, []string{"admin"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestUnassignRolesFromUser_RejectsLastAdmin(t *testing.T) {
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{{ID: otherUUID, Enabled: true}}
	s := NewService(fp)
	err := s.UnassignRolesFromUser(context.Background(), callerUUID, otherUUID, []string{"admin"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestUnassignRolesFromUser_AllowsRemovingNonAdminRole(t *testing.T) {
	fp := &fakeProvider{}
	// Even with target as only admin, removing a NON-admin role is fine.
	fp.mutationCalls.adminsForLastCheck = []User{{ID: otherUUID, Enabled: true}}
	s := NewService(fp)
	if err := s.UnassignRolesFromUser(context.Background(), callerUUID, otherUUID, []string{"editor"}); err != nil {
		t.Errorf("removing non-admin should not trigger last-admin guard, got %v", err)
	}
}

func TestUnassignRolesFromUser_NormalizesAdminCheck(t *testing.T) {
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{{ID: otherUUID, Enabled: true}}
	s := NewService(fp)
	// Mixed case "ADMIN" must still trip the guard.
	err := s.UnassignRolesFromUser(context.Background(), callerUUID, otherUUID, []string{"ADMIN"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden after normalization, got %v", err)
	}
}

// ─────────────── Stage 5.2C — Reset password / Resend ───────────────

func TestSendResetPasswordEmail_RejectsNonUUID(t *testing.T) {
	s := NewService(&fakeProvider{})
	err := s.SendResetPasswordEmail(context.Background(), "bogus")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestSendResetPasswordEmail_ForwardsToProvider(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	if err := s.SendResetPasswordEmail(context.Background(), otherUUID); err != nil {
		t.Fatalf("SendResetPasswordEmail: %v", err)
	}
	if fp.mutationCalls.resetPasswordUserID != otherUUID {
		t.Errorf("provider received %q", fp.mutationCalls.resetPasswordUserID)
	}
}

func TestResendInvitation_RejectsNonUUID(t *testing.T) {
	s := NewService(&fakeProvider{})
	_, err := s.ResendInvitation(context.Background(), "bogus")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

// ─────────────── Stage 5.2D — DeleteUser ───────────────

func TestDeleteUser_RejectsNonUUID(t *testing.T) {
	s := NewService(&fakeProvider{})
	if err := s.DeleteUser(context.Background(), callerUUID, "not-a-uuid"); !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestDeleteUser_RejectsSelfDelete(t *testing.T) {
	s := NewService(&fakeProvider{})
	err := s.DeleteUser(context.Background(), callerUUID, callerUUID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestDeleteUser_RejectsLastAdmin(t *testing.T) {
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{{ID: otherUUID, Enabled: true}}
	s := NewService(fp)
	err := s.DeleteUser(context.Background(), callerUUID, otherUUID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestDeleteUser_AllowsNonAdmin(t *testing.T) {
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{{ID: callerUUID, Enabled: true}}
	s := NewService(fp)
	if err := s.DeleteUser(context.Background(), callerUUID, otherUUID); err != nil {
		t.Errorf("deleting non-admin target should be allowed, got %v", err)
	}
	if fp.mutationCalls.deleteUserID != otherUUID {
		t.Errorf("provider received %q", fp.mutationCalls.deleteUserID)
	}
}

func TestDeleteUser_DisabledAdminsDontCount(t *testing.T) {
	// If the only enabled admin is the target, refuse — disabled admins
	// don't keep the realm administrable.
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{
		{ID: otherUUID, Enabled: true},
		{ID: thirdUUID, Enabled: false},
	}
	s := NewService(fp)
	err := s.DeleteUser(context.Background(), callerUUID, otherUUID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden (only enabled admin is target), got %v", err)
	}
}

// ─────────────── Stage 5.2D — DeleteRole ───────────────

func TestDeleteRole_RejectsProtectedNames(t *testing.T) {
	s := NewService(&fakeProvider{})
	cases := []string{"admin", "user", "default-roles-saas", "offline_access", "uma_authorization", "ADMIN"}
	for _, name := range cases {
		err := s.DeleteRole(context.Background(), name)
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("name=%q: expected ErrForbidden, got %v", name, err)
		}
	}
}

func TestDeleteRole_AllowsUserManagedRoles(t *testing.T) {
	fp := &fakeProvider{}
	s := NewService(fp)
	if err := s.DeleteRole(context.Background(), "editor"); err != nil {
		t.Errorf("expected success, got %v", err)
	}
	if fp.mutationCalls.deleteRoleName != "editor" {
		t.Errorf("provider received %q", fp.mutationCalls.deleteRoleName)
	}
}

func TestDeleteRole_RejectsMalformedNames(t *testing.T) {
	s := NewService(&fakeProvider{})
	if err := s.DeleteRole(context.Background(), ""); !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest for empty, got %v", err)
	}
}

// ─────────────── Stage 5.2D — Sessions ───────────────

func TestDeleteSession_RejectsNonUUID(t *testing.T) {
	s := NewService(&fakeProvider{})
	if err := s.DeleteSession(context.Background(), "bogus"); !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

func TestLogoutUserSessions_RejectsNonUUID(t *testing.T) {
	s := NewService(&fakeProvider{})
	if err := s.LogoutUserSessions(context.Background(), "bogus"); !errors.Is(err, ErrBadRequest) {
		t.Errorf("expected ErrBadRequest, got %v", err)
	}
}

// ─────────────── Stage 5.2D — DeleteInvitation ───────────────

func TestDeleteInvitation_RejectsSelfDelete(t *testing.T) {
	s := NewService(&fakeProvider{})
	err := s.DeleteInvitation(context.Background(), callerUUID, callerUUID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestDeleteInvitation_DoesNotConsultLastAdminCheck(t *testing.T) {
	// An invited user has no admin role yet; the last-admin guard
	// must not fire for invitations.
	fp := &fakeProvider{}
	fp.mutationCalls.adminsForLastCheck = []User{{ID: thirdUUID, Enabled: true}}
	s := NewService(fp)
	if err := s.DeleteInvitation(context.Background(), callerUUID, otherUUID); err != nil {
		t.Errorf("invitation delete should not trip last-admin guard, got %v", err)
	}
	if fp.mutationCalls.deleteUserID != otherUUID {
		t.Errorf("provider received %q", fp.mutationCalls.deleteUserID)
	}
}
