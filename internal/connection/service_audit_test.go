package connection

import (
	"errors"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
)

// The service's audit contract, without a database.
//
// These prove the CONTROL FLOW that the atomicity suite then proves the
// consequences of: every mutation asks the audit writer to record exactly one
// event, and a refused write surfaces as an error wrapping
// audit.ErrNotRecorded so the handler knows not to attempt a second one.
//
// What they cannot prove is that the domain writes were rolled back —
// fakeRunner has no transaction, and activation's TWO row updates are exactly
// the case where that distinction matters. That proof is in
// internal/auditlog/atomicity_integration_test.go.

// TestServiceAudit_ARefusedAuditWriteFailsEveryMutation.
//
// All six, including the two whose failure shape is unusual: activation touches
// two rows, and delete destroys one.
func TestServiceAudit_ARefusedAuditWriteFailsEveryMutation(t *testing.T) {
	type testCase struct {
		arrange func(*testing.T, *harness) *Connection
		act     func(*Service, *Connection) error
	}

	draft := func(t *testing.T, h *harness) *Connection {
		t.Helper()
		c, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, CreateInput{
			Name: "primary", Provider: "keycloak", BaseURL: "https://kc.example.test",
			Realm: "tenant", ClientID: "lw-conn", ClientSecret: "s3cr3t",
		}, testEvent(audit.ActionConnectionCreated))
		if err != nil {
			t.Fatalf("seed connection: %v", err)
		}
		return c
	}

	verified := func(t *testing.T, h *harness) *Connection {
		t.Helper()
		c := draft(t, h)
		if _, _, err := h.svc.Verify(ctx(), "ws_"+fixtureWorkspaceID, c.PublicID(),
			testEvent(audit.ActionConnectionVerified)); err != nil {
			t.Fatalf("seed verification: %v", err)
		}
		return c
	}

	for name, tc := range map[string]testCase{
		"connection.create": {
			func(*testing.T, *harness) *Connection { return nil },
			func(s *Service, _ *Connection) error {
				_, err := s.Create(ctx(), "ws_"+fixtureWorkspaceID, CreateInput{
					Name: "second", Provider: "keycloak", BaseURL: "https://kc.example.test",
					Realm: "tenant", ClientID: "lw-conn", ClientSecret: "s3cr3t",
				}, testEvent(audit.ActionConnectionCreated))
				return err
			}},
		"connection.update": {draft, func(s *Service, c *Connection) error {
			name := "renamed"
			_, err := s.Update(ctx(), "ws_"+fixtureWorkspaceID, c.PublicID(),
				UpdateInput{Name: &name}, testEvent(audit.ActionConnectionUpdated))
			return err
		}},
		"connection.verify": {draft, func(s *Service, c *Connection) error {
			_, _, err := s.Verify(ctx(), "ws_"+fixtureWorkspaceID, c.PublicID(),
				testEvent(audit.ActionConnectionVerified))
			return err
		}},
		"connection.activate": {verified, func(s *Service, c *Connection) error {
			_, err := s.Activate(ctx(), "ws_"+fixtureWorkspaceID, c.PublicID(),
				testEvent(audit.ActionConnectionActivated))
			return err
		}},
		"connection.retire": {draft, func(s *Service, c *Connection) error {
			_, err := s.Retire(ctx(), "ws_"+fixtureWorkspaceID, c.PublicID(),
				testEvent(audit.ActionConnectionRetired))
			return err
		}},
		"connection.delete": {draft, func(s *Service, c *Connection) error {
			return s.Delete(ctx(), "ws_"+fixtureWorkspaceID, c.PublicID(),
				testEvent(audit.ActionConnectionDeleted))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			// The fixture is built with a WORKING writer; only the operation
			// under test meets the refusing one.
			h := newHarness(t, activeWorkspaceFixture())
			c := tc.arrange(t, h)
			h.svc.audit = refusingWriter()

			err := tc.act(h.svc, c)
			if err == nil {
				t.Fatal("the mutation succeeded even though its audit row was refused")
			}
			if !errors.Is(err, audit.ErrNotRecorded) {
				t.Errorf("error = %v, want one wrapping audit.ErrNotRecorded", err)
			}
		})
	}
}

// TestServiceAudit_EveryMutationRecordsExactlyOneEvent.
//
// One event per mutation, naming the connection that was actually touched. A
// second event would double-count the most security-sensitive group in the
// system; none would leave a realm redirect unrecorded.
func TestServiceAudit_EveryMutationRecordsExactlyOneEvent(t *testing.T) {
	writer := &fakeAuditWriter{}
	h := newHarness(t, activeWorkspaceFixture())
	h.svc.audit = writer

	c, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, CreateInput{
		Name: "primary", Provider: "keycloak", BaseURL: "https://kc.example.test",
		Realm: "tenant", ClientID: "lw-conn", ClientSecret: "s3cr3t",
	}, testEvent(audit.ActionConnectionCreated))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if h.runner.count() != 1 {
		t.Errorf("opened %d transactions for one mutation, want exactly 1 — two would mean the "+
			"domain write and the audit row can commit separately", h.runner.count())
	}

	ev := writer.only(t)
	if ev.Action != audit.ActionConnectionCreated {
		t.Errorf("Action = %q, want connection.created", ev.Action)
	}
	if ev.Target.ID != c.PublicID() {
		t.Errorf("Target.ID = %q, want the created connection %q", ev.Target.ID, c.PublicID())
	}
	if ev.Workspace == "" {
		t.Error("the event names no workspace; the durable recorder would drop it")
	}
}

// TestServiceAudit_TheProviderSecretNeverEntersTheEvent.
//
// A connection holds the provider's ADMINISTRATIVE credential. The event must
// not carry it, nor the realm and base URL — those describe an operator's
// internal infrastructure to anyone holding audit:read.
func TestServiceAudit_TheProviderSecretNeverEntersTheEvent(t *testing.T) {
	const secret = "lw-sentinel-provider-secret"

	writer := &fakeAuditWriter{}
	h := newHarness(t, activeWorkspaceFixture())
	h.svc.audit = writer

	if _, err := h.svc.Create(ctx(), "ws_"+fixtureWorkspaceID, CreateInput{
		Name: "primary", Provider: "keycloak", BaseURL: "https://kc.internal.example",
		Realm: "internal-tenant", ClientID: "lw-conn", ClientSecret: secret,
	}, testEvent(audit.ActionConnectionCreated)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, ev := range writer.recorded() {
		for _, forbidden := range []string{secret, "kc.internal.example", "internal-tenant"} {
			if ev.Target.ID == forbidden || ev.Target.Name == forbidden {
				t.Errorf("%q reached the event's target", forbidden)
			}
			for key, value := range ev.Extra {
				if s, ok := value.(string); ok && s == forbidden {
					t.Errorf("%q reached the event under Extra[%q]", forbidden, key)
				}
			}
		}
	}
}
