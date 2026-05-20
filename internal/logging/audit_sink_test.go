package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
)

// captureStdLog redirects the standard `log` package output (which the
// project logger writes through) into a buffer for the duration of the
// test. The buffer is returned so the test can inspect the emitted line.
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

func TestAuditSink_EmitsPrefixedJSONLine(t *testing.T) {
	buf := captureStdLog(t)
	sink := NewAuditSink()

	ts := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	sink.Record(context.Background(), audit.Event{
		Action: audit.ActionUserUpdated,
		Actor: audit.Actor{
			Subject: "11111111-1111-1111-1111-111111111111",
			Email:   "admin@example.com",
		},
		Target: audit.Target{
			Kind: "user",
			ID:   "22222222-2222-2222-2222-222222222222",
			Name: "jane@example.com",
		},
		IP:        "10.0.0.1",
		Timestamp: ts,
	})

	got := buf.String()
	if !strings.Contains(got, " audit ") {
		t.Fatalf("expected log line to contain the %q prefix, got %q", " audit ", got)
	}

	jsonStart := strings.Index(got, "{")
	jsonEnd := strings.LastIndex(got, "}")
	if jsonStart < 0 || jsonEnd < 0 || jsonStart >= jsonEnd {
		t.Fatalf("could not locate JSON payload in %q", got)
	}
	payload := got[jsonStart : jsonEnd+1]

	var parsed audit.Event
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("emitted JSON did not round-trip into audit.Event: %v (payload=%q)", err, payload)
	}

	if parsed.Action != audit.ActionUserUpdated {
		t.Errorf("Action = %q, want %q", parsed.Action, audit.ActionUserUpdated)
	}
	if parsed.Actor.Subject != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("Actor.Subject = %q, want the admin sub", parsed.Actor.Subject)
	}
	if parsed.Target.ID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("Target.ID = %q, want the target sub", parsed.Target.ID)
	}
	if parsed.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want 10.0.0.1", parsed.IP)
	}
	if !parsed.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", parsed.Timestamp, ts)
	}
}

func TestAuditSink_StampsTimestampWhenZero(t *testing.T) {
	buf := captureStdLog(t)
	sink := NewAuditSink()

	sink.Record(context.Background(), audit.Event{
		Action: audit.ActionRoleCreated,
		Target: audit.Target{Kind: "role", ID: "support"},
	})

	out := buf.String()
	jsonStart := strings.Index(out, "{")
	jsonEnd := strings.LastIndex(out, "}")
	var parsed audit.Event
	if err := json.Unmarshal([]byte(out[jsonStart:jsonEnd+1]), &parsed); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if parsed.Timestamp.IsZero() {
		t.Fatal("expected sink to stamp Timestamp when zero")
	}
}

func TestWireDefault_InstallsSinkAndReturnsPrev(t *testing.T) {
	captureStdLog(t)

	// Install a sentinel recorder so we can verify WireDefault returned it.
	sentinel := audit.RecorderFunc(func(context.Context, audit.Event) {})
	audit.SetDefault(sentinel)
	t.Cleanup(func() { audit.SetDefault(nil) })

	prev := WireDefault()
	if prev == nil {
		t.Fatal("WireDefault returned nil for previous recorder")
	}

	// After WireDefault, audit.Default() must be an *AuditSink — not the sentinel.
	if _, ok := audit.Default().(*AuditSink); !ok {
		t.Fatalf("expected current default to be *AuditSink, got %T", audit.Default())
	}
}
