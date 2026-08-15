package connection

import (
	"errors"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Transitions (pure domain)
// ---------------------------------------------------------------------------

func TestCanActivate(t *testing.T) {
	tests := map[string]struct {
		conn *Connection
		now  time.Time
		want error
	}{
		"verified draft": {
			conn: verifiedConnection(fixtureConnID, "c"), now: testNow, want: nil,
		},
		"never verified": {
			conn: draftConnection(fixtureConnID, "c"), now: testNow, want: ErrNotVerified,
		},
		"verification failed": {
			conn: func() *Connection {
				c := draftConnection(fixtureConnID, "c")
				at := testNow
				c.Health = HealthUnhealthy
				c.LastVerifiedAt = &at
				return c
			}(), now: testNow, want: ErrNotVerified,
		},
		"verification expired": {
			conn: verifiedConnection(fixtureConnID, "c"),
			now:  testNow.Add(VerifyValidity + time.Second), want: ErrVerificationExpired,
		},
		"verification exactly at the limit still counts": {
			conn: verifiedConnection(fixtureConnID, "c"),
			now:  testNow.Add(VerifyValidity), want: nil,
		},
		"already active": {
			conn: activeConnection(fixtureConnID, "c"), now: testNow, want: ErrAlreadyActive,
		},
		"retired is terminal": {
			conn: retiredConnection(fixtureConnID, "c"), now: testNow, want: ErrRetired,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.conn.CanActivate(tc.now)
			if !errors.Is(err, tc.want) {
				t.Errorf("CanActivate = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCanActivate_StatusCheckedBeforeVerification pins the ordering: an already
// active connection reports that, not "not verified", because the caller's
// actual mistake is the second activation.
func TestCanActivate_StatusCheckedBeforeVerification(t *testing.T) {
	c := activeConnection(fixtureConnID, "c")
	c.Health = HealthUnknown
	c.LastVerifiedAt = nil

	if err := c.CanActivate(testNow); !errors.Is(err, ErrAlreadyActive) {
		t.Errorf("CanActivate = %v, want ErrAlreadyActive", err)
	}
}

func TestIsVerified(t *testing.T) {
	tests := map[string]struct {
		conn *Connection
		now  time.Time
		want bool
	}{
		"healthy and fresh":     {verifiedConnection(fixtureConnID, "c"), testNow, true},
		"healthy but stale":     {verifiedConnection(fixtureConnID, "c"), testNow.Add(2 * VerifyValidity), false},
		"never verified":        {draftConnection(fixtureConnID, "c"), testNow, false},
		"healthy with no stamp": {func() *Connection { c := draftConnection(fixtureConnID, "c"); c.Health = HealthHealthy; return c }(), testNow, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.conn.IsVerified(tc.now); got != tc.want {
				t.Errorf("IsVerified = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanRetire(t *testing.T) {
	if err := draftConnection(fixtureConnID, "c").CanRetire(); err != nil {
		t.Errorf("a draft must be retirable: %v", err)
	}
	if err := activeConnection(fixtureConnID, "c").CanRetire(); err != nil {
		t.Errorf("an active connection must be retirable: %v", err)
	}
	if err := retiredConnection(fixtureConnID, "c").CanRetire(); !errors.Is(err, ErrRetired) {
		t.Errorf("CanRetire on retired = %v, want ErrRetired", err)
	}
}

func TestCanDelete(t *testing.T) {
	if err := draftConnection(fixtureConnID, "c").CanDelete(); err != nil {
		t.Errorf("a draft must be deletable: %v", err)
	}
	if err := retiredConnection(fixtureConnID, "c").CanDelete(); err != nil {
		t.Errorf("a retired connection must be deletable: %v", err)
	}
	if err := activeConnection(fixtureConnID, "c").CanDelete(); !errors.Is(err, ErrActiveCannotDelete) {
		t.Errorf("CanDelete on active = %v, want ErrActiveCannotDelete", err)
	}
}

func TestCanUpdate(t *testing.T) {
	if err := draftConnection(fixtureConnID, "c").CanUpdate(); err != nil {
		t.Errorf("a draft must be editable: %v", err)
	}
	if err := activeConnection(fixtureConnID, "c").CanUpdate(); !errors.Is(err, ErrNotDraft) {
		t.Errorf("CanUpdate on active = %v, want ErrNotDraft", err)
	}
	if err := retiredConnection(fixtureConnID, "c").CanUpdate(); !errors.Is(err, ErrRetired) {
		t.Errorf("CanUpdate on retired = %v, want ErrRetired", err)
	}
}

func TestPublicIDs(t *testing.T) {
	c := draftConnection(fixtureConnID, "c")
	if got, want := c.PublicID(), "conn_"+fixtureConnID; got != want {
		t.Errorf("PublicID = %q, want %q", got, want)
	}
	if got, want := c.WorkspacePublicID(), "ws_"+fixtureWorkspaceID; got != want {
		t.Errorf("WorkspacePublicID = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// NewService
// ---------------------------------------------------------------------------

// TestNewService_NilCollaboratorYieldsNil pins the "not wired" signal. The
// keyring case is the one that matters operationally: no master key means no
// connection routes, rather than connections that store credentials unsealed.
func TestNewService_NilCollaboratorYieldsNil(t *testing.T) {
	repo := newFakeRepository()
	ws := newFakeWorkspaces()
	keyring := testKeyring()
	verifier := &fakeVerifier{}

	if NewService(nil, ws, keyring, verifier, &fakeRunner{}, &fakeAuditWriter{}) != nil {
		t.Error("nil repository must yield a nil service")
	}
	if NewService(repo, nil, keyring, verifier, &fakeRunner{}, &fakeAuditWriter{}) != nil {
		t.Error("nil workspace store must yield a nil service")
	}
	if NewService(repo, ws, nil, verifier, &fakeRunner{}, &fakeAuditWriter{}) != nil {
		t.Error("nil keyring must yield a nil service — credentials cannot be stored unsealed")
	}
	if NewService(repo, ws, keyring, nil, &fakeRunner{}, &fakeAuditWriter{}) != nil {
		t.Error("nil verifier must yield a nil service")
	}
	// The transactional collaborators belong in this list: a Service that fell
	// back to a non-transactional path when they were absent would make
	// atomicity conditional on wiring nobody checks.
	if NewService(repo, ws, keyring, verifier, nil, &fakeAuditWriter{}) != nil {
		t.Error("nil transaction runner must yield a nil service — atomicity is not optional")
	}
	if NewService(repo, ws, keyring, verifier, &fakeRunner{}, nil) != nil {
		t.Error("nil audit writer must yield a nil service — the audit row is not optional")
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestCreate_SealsSecretAndStartsAsDraft(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture())

	c, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, CreateInput{
		Name: "  Production Keycloak  ", BaseURL: "https://kc.example.com/", Realm: " saas ",
		ClientID: " svc ", ClientSecret: "the-secret",
	}, testEvent(audit.ActionConnectionCreated))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if c.Status != StatusDraft {
		t.Errorf("status = %q, want draft", c.Status)
	}
	if c.Provider != ProviderKeycloak {
		t.Errorf("provider = %q, want keycloak", c.Provider)
	}
	if c.Name != "Production Keycloak" {
		t.Errorf("name = %q, want it trimmed", c.Name)
	}
	if c.BaseURL != "https://kc.example.com" {
		t.Errorf("base_url = %q, want the trailing slash stripped", c.BaseURL)
	}
	if c.Realm != "saas" || c.ClientID != "svc" {
		t.Errorf("realm/client_id = %q/%q, want them trimmed", c.Realm, c.ClientID)
	}
	if c.Health != HealthUnknown || c.AccessMode != AccessModeUnknown || c.LastVerifiedAt != nil {
		t.Errorf("a new connection must be unverified, got health=%q mode=%q at=%v",
			c.Health, c.AccessMode, c.LastVerifiedAt)
	}

	// The stored secret must be sealed, openable only with the right AAD.
	sealed, err := h.repo.OpenSecret(ctx(), c.ID)
	if err != nil || sealed == nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	if strings.Contains(string(sealed.Ciphertext), "the-secret") {
		t.Error("stored ciphertext contains the plaintext secret")
	}
	opened, err := h.keyring.Open(*sealed, secretAAD(c.ID))
	if err != nil {
		t.Fatalf("open stored secret: %v", err)
	}
	if string(opened) != "the-secret" {
		t.Errorf("opened secret = %q, want the original", opened)
	}
	// Bound to this connection: another connection's AAD must not open it.
	if _, err := h.keyring.Open(*sealed, secretAAD(fixtureConnID2)); err == nil {
		t.Error("the sealed secret opened under another connection's AAD")
	}
}

// TestCreate_DoesNotTrimTheSecret — whitespace may be part of a credential, and
// silently altering one produces a failure that looks like the provider's fault.
func TestCreate_DoesNotTrimTheSecret(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture())

	c, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, CreateInput{
		Name: "n", BaseURL: "https://kc.example.com", Realm: "saas", ClientID: "svc",
		ClientSecret: "  padded-secret  ",
	}, testEvent(audit.ActionConnectionCreated))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sealed, _ := h.repo.OpenSecret(ctx(), c.ID)
	opened, err := h.keyring.Open(*sealed, secretAAD(c.ID))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != "  padded-secret  " {
		t.Errorf("stored secret = %q, want it byte-identical to the input", opened)
	}
}

func TestCreate_ValidationErrors(t *testing.T) {
	valid := CreateInput{Name: "n", BaseURL: "https://kc.example.com", Realm: "saas", ClientID: "svc", ClientSecret: "s"}

	tests := map[string]struct {
		mutate func(CreateInput) CreateInput
		want   error
	}{
		"blank name":        {func(i CreateInput) CreateInput { i.Name = "   "; return i }, ErrNameRequired},
		"overlong name":     {func(i CreateInput) CreateInput { i.Name = strings.Repeat("a", maxNameLength+1); return i }, ErrInvalidRequest},
		"empty base url":    {func(i CreateInput) CreateInput { i.BaseURL = ""; return i }, ErrBaseURLInvalid},
		"relative base url": {func(i CreateInput) CreateInput { i.BaseURL = "/realms/saas"; return i }, ErrBaseURLInvalid},
		"no scheme":         {func(i CreateInput) CreateInput { i.BaseURL = "kc.example.com"; return i }, ErrBaseURLInvalid},
		"ftp scheme":        {func(i CreateInput) CreateInput { i.BaseURL = "ftp://kc.example.com"; return i }, ErrBaseURLInvalid},
		"no host":           {func(i CreateInput) CreateInput { i.BaseURL = "https://"; return i }, ErrBaseURLInvalid},
		"blank realm":       {func(i CreateInput) CreateInput { i.Realm = "  "; return i }, ErrRealmRequired},
		"blank client id":   {func(i CreateInput) CreateInput { i.ClientID = "  "; return i }, ErrClientIDRequired},
		"empty secret":      {func(i CreateInput) CreateInput { i.ClientSecret = ""; return i }, ErrClientSecretRequired},
		"unknown provider":  {func(i CreateInput) CreateInput { i.Provider = "okta"; return i }, ErrProviderUnsupported},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, activeWorkspaceFixture())
			_, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, tc.mutate(valid), testEvent(audit.ActionConnectionCreated))
			if !errors.Is(err, tc.want) {
				t.Errorf("Create = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCreate_WorkspaceErrors(t *testing.T) {
	valid := CreateInput{Name: "n", BaseURL: "https://kc.example.com", Realm: "saas", ClientID: "svc", ClientSecret: "s"}

	t.Run("archived workspace refuses new connections", func(t *testing.T) {
		h := newHarness(t, archivedWorkspaceFixture())
		_, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, valid, testEvent(audit.ActionConnectionCreated))
		if !errors.Is(err, ErrWorkspaceArchived) {
			t.Errorf("Create = %v, want ErrWorkspaceArchived", err)
		}
	})

	t.Run("unknown workspace", func(t *testing.T) {
		h := newHarness(t, activeWorkspaceFixture())
		_, err := h.svc.Create(ctx(), "ws_"+fixtureConnID2, valid, testEvent(audit.ActionConnectionCreated))
		if !errors.Is(err, ErrWorkspaceNotFound) {
			t.Errorf("Create = %v, want ErrWorkspaceNotFound", err)
		}
	})

	t.Run("malformed workspace id", func(t *testing.T) {
		h := newHarness(t, activeWorkspaceFixture())
		for _, id := range []string{"conn_" + fixtureWorkspaceID, "nonsense", "ws_"} {
			_, err := h.svc.Create(ctx(), id, valid, testEvent(audit.ActionConnectionCreated))
			if !errors.Is(err, ErrInvalidWorkspaceID) {
				t.Errorf("Create(%q) = %v, want ErrInvalidWorkspaceID", id, err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Ownership scoping
// ---------------------------------------------------------------------------

// TestGet_RejectsConnectionFromAnotherWorkspace is what makes the nested route
// mean something. Reported as not-found rather than a distinct error: from this
// caller's position it does not exist, and saying more would confirm it exists
// elsewhere.
func TestGet_RejectsConnectionFromAnotherWorkspace(t *testing.T) {
	other := draftConnection(fixtureConnID, "c")
	other.WorkspaceID = "99999999-9999-4999-8999-999999999999"

	h := newHarness(t, activeWorkspaceFixture(), other)

	_, err := h.svc.Get(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get across workspaces = %v, want ErrNotFound", err)
	}
}

func TestGet_InvalidConnectionIDNeverReachesTheRepository(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture())
	h.repo.failWith = errors.New("repository must not be reached")

	for name, id := range map[string]string{
		"workspace prefix": "ws_" + fixtureConnID,
		"no prefix parts":  "garbage",
		"empty":            "",
		"prefix only":      "conn_",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.svc.Get(ctx(), "ws_"+fixtureWorkspaceID, id)
			if !errors.Is(err, ErrInvalidID) {
				t.Errorf("Get(%q) = %v, want ErrInvalidID", id, err)
			}
		})
	}
}

func TestGet_MissingIsNotFound(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture())
	if _, err := h.svc.Get(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestList_DefaultsToAllStatuses(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(),
		draftConnection(fixtureConnID, "A draft"),
		activeConnection(fixtureConnID2, "B active"),
	)

	items, err := h.svc.List(ctx(), "ws_"+fixtureWorkspaceID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("default listing returned %d items, want 2 — drafts and retired must be visible", len(items))
	}
	if items[0].Name != "A draft" {
		t.Errorf("ordering is not by name: %+v", items)
	}
}

func TestList_Filters(t *testing.T) {
	seed := []*Connection{
		draftConnection(fixtureConnID, "A"),
		activeConnection(fixtureConnID2, "B"),
	}
	for filter, want := range map[string]int{"draft": 1, "active": 1, "retired": 0, "all": 2} {
		t.Run(filter, func(t *testing.T) {
			h := newHarness(t, activeWorkspaceFixture(), seed...)
			items, err := h.svc.List(ctx(), "ws_"+fixtureWorkspaceID, filter)
			if err != nil {
				t.Fatalf("List(%q): %v", filter, err)
			}
			if len(items) != want {
				t.Errorf("List(%q) returned %d, want %d", filter, len(items), want)
			}
		})
	}
}

func TestList_RejectsUnknownFilter(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture())
	if _, err := h.svc.List(ctx(), "ws_"+fixtureWorkspaceID, "archived"); !errors.Is(err, ErrInvalidStatusFilter) {
		t.Errorf("List = %v, want ErrInvalidStatusFilter", err)
	}
}

// TestList_WorksOnArchivedWorkspace — reading is not writing. An archived
// workspace's connections stay inspectable.
func TestList_WorksOnArchivedWorkspace(t *testing.T) {
	h := newHarness(t, archivedWorkspaceFixture(), draftConnection(fixtureConnID, "A"))
	if _, err := h.svc.List(ctx(), "ws_"+fixtureWorkspaceID, ""); err != nil {
		t.Errorf("List on an archived workspace: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestUpdate_ResetsVerificationWhenProbedFieldsChange(t *testing.T) {
	newRealm := "other-realm"
	h := newHarness(t, activeWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"))

	updated, err := h.svc.Update(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID,
		UpdateInput{Realm: &newRealm}, testEvent(audit.ActionConnectionUpdated))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Realm != "other-realm" {
		t.Errorf("realm = %q", updated.Realm)
	}
	if updated.Health != HealthUnknown || updated.LastVerifiedAt != nil {
		t.Error("changing a probed field must reset the verification — otherwise the " +
			"connection could be activated on the strength of a probe against a different provider")
	}
	if updated.IsVerified(testNow) {
		t.Error("connection still reports verified after its coordinates changed")
	}
}

// TestUpdate_NameOnlyKeepsVerification — renaming does not change what would be
// probed, so the verdict stands.
func TestUpdate_NameOnlyKeepsVerification(t *testing.T) {
	newName := "Renamed"
	h := newHarness(t, activeWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"))

	updated, err := h.svc.Update(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID,
		UpdateInput{Name: &newName}, testEvent(audit.ActionConnectionUpdated))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Errorf("name = %q", updated.Name)
	}
	if updated.Health != HealthHealthy || !updated.IsVerified(testNow) {
		t.Error("a rename must not invalidate the verification")
	}
}

func TestUpdate_SecretChangeResealsAndResets(t *testing.T) {
	newSecret := "rotated-secret"
	h := newHarness(t, activeWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"))

	updated, err := h.svc.Update(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID,
		UpdateInput{ClientSecret: &newSecret}, testEvent(audit.ActionConnectionUpdated))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Health != HealthUnknown {
		t.Error("rotating the secret must reset the verification")
	}

	sealed, _ := h.repo.OpenSecret(ctx(), fixtureConnID)
	opened, err := h.keyring.Open(*sealed, secretAAD(fixtureConnID))
	if err != nil {
		t.Fatalf("open resealed secret: %v", err)
	}
	if string(opened) != newSecret {
		t.Errorf("stored secret = %q, want the rotated value", opened)
	}
}

func TestUpdate_RefusesNonDraft(t *testing.T) {
	name := "x"

	t.Run("active", func(t *testing.T) {
		h := newHarness(t, activeWorkspaceFixture(), activeConnection(fixtureConnID, "c"))
		_, err := h.svc.Update(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, UpdateInput{Name: &name}, testEvent(audit.ActionConnectionUpdated))
		if !errors.Is(err, ErrNotDraft) {
			t.Errorf("Update on active = %v, want ErrNotDraft", err)
		}
	})

	t.Run("retired", func(t *testing.T) {
		h := newHarness(t, activeWorkspaceFixture(), retiredConnection(fixtureConnID, "c"))
		_, err := h.svc.Update(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, UpdateInput{Name: &name}, testEvent(audit.ActionConnectionUpdated))
		if !errors.Is(err, ErrRetired) {
			t.Errorf("Update on retired = %v, want ErrRetired", err)
		}
	})
}

func TestUpdate_ValidatesFields(t *testing.T) {
	blank := "   "
	bad := "not-a-url"
	empty := ""

	tests := map[string]struct {
		in   UpdateInput
		want error
	}{
		"blank name":   {UpdateInput{Name: &blank}, ErrNameRequired},
		"bad base url": {UpdateInput{BaseURL: &bad}, ErrBaseURLInvalid},
		"blank realm":  {UpdateInput{Realm: &blank}, ErrRealmRequired},
		"blank client": {UpdateInput{ClientID: &blank}, ErrClientIDRequired},
		"empty secret": {UpdateInput{ClientSecret: &empty}, ErrClientSecretRequired},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))
			_, err := h.svc.Update(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, tc.in, testEvent(audit.ActionConnectionUpdated))
			if !errors.Is(err, tc.want) {
				t.Errorf("Update = %v, want %v", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

func TestVerify_PassesTheOpenedSecretToTheProbe(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))

	_, _, err := h.svc.Verify(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionVerified))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	target, ok := h.verifier.lastTarget()
	if !ok {
		t.Fatal("the verifier was never called")
	}
	if target.ClientSecret != "seeded-secret" {
		t.Errorf("probe received secret %q, want the opened plaintext", target.ClientSecret)
	}
	if target.BaseURL != "https://kc.example.com" || target.Realm != "saas" || target.ClientID != "svc" {
		t.Errorf("probe received the wrong coordinates: %+v", target)
	}
}

func TestVerify_RecordsSuccess(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))

	updated, report, err := h.svc.Verify(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionVerified))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.OK {
		t.Fatal("report should be OK")
	}
	if updated.Health != HealthHealthy {
		t.Errorf("health = %q, want healthy", updated.Health)
	}
	if updated.AccessMode != AccessModeFull {
		t.Errorf("access mode = %q, want full", updated.AccessMode)
	}
	if updated.LastVerifiedAt == nil {
		t.Fatal("last_verified_at must be stamped")
	}
	if !updated.IsVerified(testNow) {
		t.Error("connection should report verified immediately after a successful probe")
	}
}

func TestVerify_RecordsFailure(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"))
	h.verifier.report = failedReport()

	updated, report, err := h.svc.Verify(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionVerified))
	if err != nil {
		t.Fatalf("Verify must not error when the PROBE fails — that is a verdict: %v", err)
	}
	if report.OK {
		t.Fatal("report should not be OK")
	}
	if updated.Health != HealthUnhealthy {
		t.Errorf("health = %q, want unhealthy", updated.Health)
	}
	if updated.IsVerified(testNow) {
		t.Error("a failed probe must clear the verified state")
	}
	// And that state must block activation.
	if err := updated.CanActivate(testNow); !errors.Is(err, ErrNotVerified) {
		t.Errorf("CanActivate after a failed probe = %v, want ErrNotVerified", err)
	}
}

// TestVerify_UnopenableSecretIsAnInternalError — a stored secret that will not
// open means the master key is wrong or the row was tampered with. That is an
// operator emergency, not a statement about the provider, so it must not be
// recorded as an unhealthy verdict.
func TestVerify_UnopenableSecretIsAnInternalError(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))

	// Replace the stored secret with one sealed under a different AAD.
	wrong, err := h.keyring.Seal([]byte("x"), secretAAD("some-other-connection"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	h.repo.secrets[fixtureConnID] = wrong

	_, _, err = h.svc.Verify(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionVerified))
	if err == nil {
		t.Fatal("Verify succeeded with an unopenable secret")
	}
	var domainErr *Error
	if errors.As(err, &domainErr) {
		t.Errorf("error = %v; an unopenable secret must NOT be a domain verdict", domainErr.Code)
	}

	stored, _ := h.repo.GetByID(ctx(), fixtureConnID)
	if stored.Health != HealthUnknown {
		t.Errorf("health moved to %q; the provider was never probed", stored.Health)
	}
}

// ---------------------------------------------------------------------------
// Activate
// ---------------------------------------------------------------------------

func TestActivate_PromotesAndRetiresIncumbent(t *testing.T) {
	incumbent := activeConnection(fixtureConnID2, "Old")
	candidate := verifiedConnection(fixtureConnID, "New")
	h := newHarness(t, activeWorkspaceFixture(), incumbent, candidate)

	activated, err := h.svc.Activate(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionActivated))
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activated.Status != StatusActive {
		t.Errorf("status = %q, want active", activated.Status)
	}
	if activated.ActivatedAt == nil {
		t.Error("activated_at must be stamped")
	}

	old, _ := h.repo.GetByID(ctx(), fixtureConnID2)
	if old.Status != StatusRetired {
		t.Errorf("the incumbent is %q, want retired — activation must swap atomically", old.Status)
	}
	if old.RetiredAt == nil {
		t.Error("the retired incumbent must be stamped")
	}
}

func TestActivate_RefusesUnverified(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))
	_, err := h.svc.Activate(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionActivated))
	if !errors.Is(err, ErrNotVerified) {
		t.Errorf("Activate = %v, want ErrNotVerified", err)
	}
}

func TestActivate_RefusesExpiredVerification(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"))
	h.svc.now = func() time.Time { return testNow.Add(VerifyValidity + time.Minute) }

	_, err := h.svc.Activate(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionActivated))
	if !errors.Is(err, ErrVerificationExpired) {
		t.Errorf("Activate = %v, want ErrVerificationExpired", err)
	}
}

func TestActivate_RefusesArchivedWorkspace(t *testing.T) {
	h := newHarness(t, archivedWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"))
	_, err := h.svc.Activate(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionActivated))
	if !errors.Is(err, ErrWorkspaceArchived) {
		t.Errorf("Activate in an archived workspace = %v, want ErrWorkspaceArchived", err)
	}
}

func TestActivate_RefusesRepeat(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), activeConnection(fixtureConnID, "c"))
	_, err := h.svc.Activate(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionActivated))
	if !errors.Is(err, ErrAlreadyActive) {
		t.Errorf("re-activating = %v, want ErrAlreadyActive (deliberately not idempotent)", err)
	}
}

func TestActivate_RefusesRetired(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), retiredConnection(fixtureConnID, "c"))
	_, err := h.svc.Activate(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionActivated))
	if !errors.Is(err, ErrRetired) {
		t.Errorf("Activate on retired = %v, want ErrRetired", err)
	}
}

// ---------------------------------------------------------------------------
// Retire / Delete
// ---------------------------------------------------------------------------

func TestRetire(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), activeConnection(fixtureConnID, "c"))

	retired, err := h.svc.Retire(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionRetired))
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if retired.Status != StatusRetired || retired.RetiredAt == nil {
		t.Errorf("status/retired_at = %q/%v", retired.Status, retired.RetiredAt)
	}
	// activated_at is history worth keeping.
	if retired.ActivatedAt == nil {
		t.Error("retiring must not erase activated_at")
	}
}

func TestRetire_RefusesRepeat(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture(), retiredConnection(fixtureConnID, "c"))
	_, err := h.svc.Retire(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionRetired))
	if !errors.Is(err, ErrRetired) {
		t.Errorf("Retire on retired = %v, want ErrRetired", err)
	}
}

func TestDelete(t *testing.T) {
	tests := map[string]struct {
		conn *Connection
		want error
	}{
		"draft":   {draftConnection(fixtureConnID, "c"), nil},
		"retired": {retiredConnection(fixtureConnID, "c"), nil},
		"active":  {activeConnection(fixtureConnID, "c"), ErrActiveCannotDelete},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, activeWorkspaceFixture(), tc.conn)
			err := h.svc.Delete(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID, testEvent(audit.ActionConnectionDeleted))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Delete = %v, want %v", err, tc.want)
			}

			_, getErr := h.svc.Get(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID)
			if tc.want == nil {
				if !errors.Is(getErr, ErrNotFound) {
					t.Error("the connection survived a successful delete")
				}
				// The sealed secret must go with it.
				if _, ok := h.repo.secrets[fixtureConnID]; ok {
					t.Error("the sealed secret outlived the connection row")
				}
			} else if getErr != nil {
				t.Errorf("a refused delete must leave the connection readable: %v", getErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Failure propagation
// ---------------------------------------------------------------------------

// TestService_PropagatesRepositoryFailures pins that unexpected storage errors
// pass up unchanged so the handler can log the cause and answer internal_error.
func TestService_PropagatesRepositoryFailures(t *testing.T) {
	calls := map[string]func(*harness) error{
		"Get": func(h *harness) error {
			_, err := h.svc.Get(ctx(), "ws_"+fixtureWorkspaceID, "conn_"+fixtureConnID)
			return err
		},
		"List": func(h *harness) error { _, err := h.svc.List(ctx(), "ws_"+fixtureWorkspaceID, ""); return err },
		"Create": func(h *harness) error {
			_, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, CreateInput{
				Name: "n", BaseURL: "https://kc.example.com", Realm: "r", ClientID: "c", ClientSecret: "s"}, testEvent(audit.ActionConnectionCreated))
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))
			h.repo.failWith = errBoom
			if err := call(h); !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want the repository's error unwrapped", err)
			}
		})
	}
}

func TestService_PropagatesWorkspaceStoreFailures(t *testing.T) {
	h := newHarness(t, activeWorkspaceFixture())
	h.workspaces.failWith = errBoom

	if _, err := h.svc.List(ctx(), "ws_"+fixtureWorkspaceID, ""); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want the workspace store's error unwrapped", err)
	}
}

func TestParseStatusFilter(t *testing.T) {
	for in, want := range map[string]StatusFilter{
		"": FilterAll, "all": FilterAll, "draft": FilterDraft,
		"active": FilterActive, "retired": FilterRetired,
	} {
		got, err := ParseStatusFilter(in)
		if err != nil || got != want {
			t.Errorf("ParseStatusFilter(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	if _, err := ParseStatusFilter("archived"); !errors.Is(err, ErrInvalidStatusFilter) {
		t.Errorf("ParseStatusFilter(\"archived\") = %v, want ErrInvalidStatusFilter", err)
	}
}
