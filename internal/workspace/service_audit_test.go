package workspace

import (
	"errors"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
)

// The service's audit contract, without a database.
//
// These prove the CONTROL FLOW that the atomicity suite then proves the
// consequences of:
//
//	every mutation asks the audit writer to record exactly one event
//	the event is completed from the service's OWN result, not the request
//	a refused audit write surfaces as an error wrapping audit.ErrNotRecorded
//	the caller never receives a success it did not get
//
// What they cannot prove is that the domain write was rolled back — fakeRunner
// has no transaction. That is PostgreSQL's statement to make, and it is made in
// internal/auditlog/atomicity_integration_test.go.

// TestServiceAudit_EveryMutationRecordsExactlyOneEvent.
//
// Table-driven over the three mutations so a fourth added later has an obvious
// place to go — and so "this domain records what it does" is one assertion
// rather than three that could drift apart.
func TestServiceAudit_EveryMutationRecordsExactlyOneEvent(t *testing.T) {
	seed := activeWorkspace("3f2504e0-4f89-41d3-9a0c-0305e82c3301", "alpha", "Alpha")

	cases := map[string]struct {
		action audit.Action
		call   func(*Service, *audit.Event) error
	}{
		"create": {audit.ActionWorkspaceCreated, func(s *Service, ev *audit.Event) error {
			_, err := s.Create(ctx(), CreateInput{Name: "New"}, ev)
			return err
		}},
		"rename": {audit.ActionWorkspaceRenamed, func(s *Service, ev *audit.Event) error {
			_, err := s.UpdateName(ctx(), "ws_"+seed.ID, "Renamed", ev)
			return err
		}},
		"archive": {audit.ActionWorkspaceArchived, func(s *Service, ev *audit.Event) error {
			_, err := s.Archive(ctx(), "ws_"+seed.ID, ev)
			return err
		}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			writer := &fakeAuditWriter{}
			svc, runner := newAuditedService(newFakeRepository(activeWorkspace(seed.ID, seed.Slug, seed.Name)), writer)

			if err := tc.call(svc, testEvent(tc.action)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if runner.count() != 1 {
				t.Errorf("opened %d transactions, want exactly 1", runner.count())
			}

			ev := writer.only(t)
			if ev.Action != tc.action {
				t.Errorf("Action = %q, want %q", ev.Action, tc.action)
			}
			// Completed by the SERVICE from its own result. A skeleton that
			// reached the writer unfilled would produce a row naming no
			// workspace, which the durable recorder drops entirely.
			if ev.Workspace == "" {
				t.Error("the event names no workspace; the durable recorder would drop it")
			}
			if ev.Target.ID == "" {
				t.Error("the event names no target")
			}
			if ev.RequestID != "req-fixture-1" {
				t.Errorf("RequestID = %q, want the request's", ev.RequestID)
			}
		})
	}
}

// TestServiceAudit_ARefusedAuditWriteFailsTheMutation.
//
// The caller must not be told a mutation succeeded when its record could not be
// written — and the error must wrap the sentinel, or the handler cannot tell it
// from a domain failure and would answer by attempting a second audit write.
func TestServiceAudit_ARefusedAuditWriteFailsTheMutation(t *testing.T) {
	seed := activeWorkspace("3f2504e0-4f89-41d3-9a0c-0305e82c3301", "alpha", "Alpha")

	for name, call := range map[string]func(*Service) error{
		"create": func(s *Service) error {
			_, err := s.Create(ctx(), CreateInput{Name: "New"}, testEvent(audit.ActionWorkspaceCreated))
			return err
		},
		"rename": func(s *Service) error {
			_, err := s.UpdateName(ctx(), "ws_"+seed.ID, "Renamed", testEvent(audit.ActionWorkspaceRenamed))
			return err
		},
		"archive": func(s *Service) error {
			_, err := s.Archive(ctx(), "ws_"+seed.ID, testEvent(audit.ActionWorkspaceArchived))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newAuditedService(
				newFakeRepository(activeWorkspace(seed.ID, seed.Slug, seed.Name)), refusingWriter())

			err := call(svc)
			if err == nil {
				t.Fatal("the mutation succeeded even though its audit row was refused")
			}
			if !errors.Is(err, audit.ErrNotRecorded) {
				t.Errorf("error = %v, want one wrapping audit.ErrNotRecorded", err)
			}
		})
	}
}

// TestServiceAudit_NoEventIsRecordedForAMutationThatDidNotHappen.
//
// A rename of a workspace that does not exist writes nothing, so it must record
// nothing: an audit row for a mutation that never occurred is worse than a
// missing one, because it is believed.
func TestServiceAudit_NoEventIsRecordedForAMutationThatDidNotHappen(t *testing.T) {
	writer := &fakeAuditWriter{}
	svc, _ := newAuditedService(newFakeRepository(), writer)

	if _, err := svc.Archive(ctx(), "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		testEvent(audit.ActionWorkspaceArchived)); err == nil {
		t.Fatal("archiving an absent workspace succeeded")
	}
	if got := writer.recorded(); len(got) != 0 {
		t.Errorf("recorded %d event(s) for a mutation that did not happen: %+v", len(got), got)
	}
}
