package project

import (
	"context"
	"errors"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

// The service's audit contract, without a database.
//
// These prove the CONTROL FLOW that the atomicity suite then proves the
// consequences of: every mutation asks the audit writer to record exactly one
// event, the event is completed from the service's own result, and a refused
// write surfaces as an error wrapping audit.ErrNotRecorded.
//
// What they cannot prove is that the domain write was rolled back — fakeRunner
// has no transaction. That is PostgreSQL's statement to make, and it is made in
// internal/auditlog/atomicity_integration_test.go.

// newAuditedProjectService builds a Service whose audit writer the test owns.
func newAuditedProjectService(t *testing.T, writer *fakeAuditWriter) (*Service, *fakeRepo, *fakeRunner) {
	t.Helper()
	repo := newFakeRepo()
	workspaces := newFakeWorkspaces()
	workspaces.add(testWorkspaceUUID, workspace.StatusActive)

	runner := &fakeRunner{}
	svc := NewService(repo, workspaces, runner, writer)
	if svc == nil {
		t.Fatal("NewService returned nil with every collaborator present")
	}
	return svc, repo, runner
}

// seedAuditedProject creates a project through the service, failing the test if it
// cannot — a seed that silently failed would make the assertion below about the
// wrong call.
func seedAuditedProject(t *testing.T, s *Service) *Project {
	t.Helper()
	p, err := s.Create(context.Background(), wsID(), "Worker", testEvent(audit.ActionProjectCreated))
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p
}

// TestServiceAudit_ARefusedAuditWriteFailsEveryMutation.
//
// All five, because they fail in different shapes — an insert, two updates, an
// insert that also mints a secret, and an update that is a kill switch — and an
// implementation that got one right could get another wrong.
//
// The arrange/act split matters: the fixture is built with a WORKING writer and
// only the operation under test meets the refusing one. Seeding through the
// refusing writer would fail the seed, and the assertion would be about the
// wrong call.
func TestServiceAudit_ARefusedAuditWriteFailsEveryMutation(t *testing.T) {
	type testCase struct {
		arrange func(*testing.T, *Service) *Project
		act     func(*Service, *Project) error
	}

	noSeed := func(*testing.T, *Service) *Project { return nil }

	for name, tc := range map[string]testCase{
		"project.create": {noSeed, func(s *Service, _ *Project) error {
			_, err := s.Create(context.Background(), wsID(), "Worker",
				testEvent(audit.ActionProjectCreated))
			return err
		}},
		"project.rename": {seedAuditedProject, func(s *Service, p *Project) error {
			_, err := s.Rename(context.Background(), wsID(), p.PublicID(), "Renamed",
				testEvent(audit.ActionProjectRenamed))
			return err
		}},
		"project.archive": {seedAuditedProject, func(s *Service, p *Project) error {
			_, err := s.Archive(context.Background(), wsID(), p.PublicID(),
				testEvent(audit.ActionProjectArchived))
			return err
		}},
		"credential.create": {seedAuditedProject, func(s *Service, p *Project) error {
			_, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(),
				CreateCredentialInput{Label: "deploy", Scopes: []string{"users:read"}},
				testEvent(audit.ActionCredentialCreated))
			return err
		}},
		"credential.revoke": {seedAuditedProject, func(s *Service, p *Project) error {
			// The credential itself is seeded here, still with the working
			// writer, because revoking one that does not exist would fail for
			// the wrong reason.
			cred, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(),
				CreateCredentialInput{Label: "live", Scopes: []string{"users:read"}},
				testEvent(audit.ActionCredentialCreated))
			if err != nil {
				return err
			}
			s.audit = refusingWriter()
			_, err = s.RevokeCredential(context.Background(), wsID(), p.PublicID(),
				cred.PublicID(), "operator-1", testEvent(audit.ActionCredentialRevoked))
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := newAuditedProjectService(t, &fakeAuditWriter{})
			p := tc.arrange(t, svc)

			// credential.revoke swaps the writer itself, after its own seed.
			if name != "credential.revoke" {
				svc.audit = refusingWriter()
			}

			err := tc.act(svc, p)
			if err == nil {
				t.Fatal("the mutation succeeded even though its audit row was refused")
			}
			if !errors.Is(err, audit.ErrNotRecorded) {
				t.Errorf("error = %v, want one wrapping audit.ErrNotRecorded", err)
			}
		})
	}
}

// TestServiceAudit_CredentialCreationReturnsNoTokenWhenTheAuditWriteFails.
//
// The sharpest consequence in this domain. The plaintext is returned exactly
// once; handing it back alongside an error would give the caller a token they
// might try to use, for a row that was rolled back.
func TestServiceAudit_CredentialCreationReturnsNoTokenWhenTheAuditWriteFails(t *testing.T) {
	working := &fakeAuditWriter{}
	svc, _, _ := newAuditedProjectService(t, working)
	p := seedAuditedProject(t, svc)

	svc.audit = refusingWriter()

	cred, token, err := svc.CreateCredential(context.Background(), wsID(), p.PublicID(),
		CreateCredentialInput{Label: "doomed", Scopes: []string{"users:read"}},
		testEvent(audit.ActionCredentialCreated))

	if err == nil {
		t.Fatal("CreateCredential succeeded with a refused audit write")
	}
	if token != "" {
		t.Error("a plaintext token was returned alongside the error; the caller could try to use it")
	}
	if cred != nil {
		t.Error("a credential was returned alongside the error")
	}
}

// TestServiceAudit_TheCredentialSecretNeverEntersTheEvent.
//
// Asserted at the service, not only at the handler: the service is where the
// token exists, so it is where the mistake would be made.
func TestServiceAudit_TheCredentialSecretNeverEntersTheEvent(t *testing.T) {
	writer := &fakeAuditWriter{}
	svc, _, _ := newAuditedProjectService(t, writer)
	p := seedAuditedProject(t, svc)

	_, token, err := svc.CreateCredential(context.Background(), wsID(), p.PublicID(),
		CreateCredentialInput{Label: "deploy", Scopes: []string{"users:read"}},
		testEvent(audit.ActionCredentialCreated))
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if token == "" {
		t.Fatal("no token was returned; the search below would pass vacuously")
	}

	// Two events were recorded: the project seed and the credential. Only the
	// credential's can carry the token, so that is the one examined.
	events := writer.recorded()
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2 (the seeded project and the credential)", len(events))
	}
	ev := events[1]

	for key, value := range ev.Extra {
		if s, ok := value.(string); ok && s == token {
			t.Errorf("the credential secret reached the event under Extra[%q]", key)
		}
	}
	if ev.Target.Name == token || ev.Target.ID == token {
		t.Error("the credential secret reached the event's target")
	}
	// The scopes DO belong there: they are what an operator needs when working
	// out what a leaked key could do.
	if _, ok := ev.Extra["scopes"]; !ok && len(ev.Extra) > 0 {
		t.Error("the granted scopes are absent from the credential event")
	}
}

// TestServiceAudit_EachMutationOpensExactlyOneTransaction.
//
// Two transactions would mean the domain write and the audit row could commit
// separately — which is the property this slice exists to remove, expressed as
// a count rather than as an outcome.
func TestServiceAudit_EachMutationOpensExactlyOneTransaction(t *testing.T) {
	writer := &fakeAuditWriter{}
	svc, _, runner := newAuditedProjectService(t, writer)

	if _, err := svc.Create(context.Background(), wsID(), "Worker",
		testEvent(audit.ActionProjectCreated)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if runner.count() != 1 {
		t.Errorf("opened %d transactions for one mutation, want exactly 1", runner.count())
	}

	ev := writer.only(t)
	if ev.Action != audit.ActionProjectCreated {
		t.Errorf("Action = %q, want project.created", ev.Action)
	}
}
