package logger

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// forgedEntry is the payload the escaping exists to defeat.
//
// It is shaped like this logger's own output, because that is what makes the
// attack work: once a newline survives into the stream, the remainder is not
// "part of a message that happens to contain punctuation", it is a second
// record, and nothing downstream can tell it from one the application wrote.
const forgedEntry = "attacker@example.com\n2026-01-01 00:00:00 [ INFO  ] [ auth      ] admin session granted"

// TestFormat_EscapesLineTerminators is the load-bearing test of this file.
//
// The real path it stands for: an email arrives in a request body, is handed
// back unvalidated when the identity read-back fails, and is concatenated into
// a log line. Neither end of that path is a good place to fix the problem:
// one is a remote system's validation, the other is one call site out of
// dozens. So the invariant is asserted here, where every message passes.
func TestFormat_EscapesLineTerminators(t *testing.T) {
	l := New("auth")

	for _, tc := range []struct {
		name string
		msg  string
	}{
		{"newline", forgedEntry},
		{"carriage return", "user=x\rfake entry"},
		{"crlf", "user=x\r\nfake entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := l.format("INFO", bgBlue, textWhite, tc.msg)

			if strings.ContainsAny(got, "\n\r") {
				t.Fatalf("format() emitted a line terminator; the message can be split into two records:\n%q", got)
			}
			// Escaped, not dropped: that the value arrived with a newline in
			// it is the fact an investigator needs.
			if !strings.Contains(got, `\n`) && !strings.Contains(got, `\r`) {
				t.Errorf("format() removed the terminator instead of escaping it, destroying the evidence:\n%q", got)
			}
		})
	}
}

// TestFormat_EscapesANSI covers the second half of the same problem.
//
// This logger writes colour codes, so its output is read by terminals. An ESC
// that survives into a message is executed by the terminal of whoever reads
// the log, and can repaint or erase the lines around it, including the real
// entry the forged one is meant to hide.
func TestFormat_EscapesANSI(t *testing.T) {
	l := New("auth")

	// The logger's own styling contributes exactly three: background, text
	// colour and reset. Any fourth came from the message.
	const ownEscapes = 3

	got := l.format("INFO", bgBlue, textWhite, "user=\x1b[2K\x1b[Ainnocent")

	if n := strings.Count(got, "\x1b"); n != ownEscapes {
		t.Errorf("format() output carries %d ESC characters, want %d; the message smuggled %d through:\n%q",
			n, ownEscapes, n-ownEscapes, got)
	}
	if !strings.Contains(got, `\x1b`) {
		t.Errorf("the ESC was dropped rather than escaped:\n%q", got)
	}
}

// TestLevels_EmitExactlyOneLine pins the property through the public methods
// rather than through format(), so that a level added later without routing
// its message through format() fails here.
//
// Fatal is absent on purpose: it calls os.Exit, and a test that survived it
// would not be testing Fatal.
func TestLevels_EmitExactlyOneLine(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(nil) })

	l := New("auth")

	for _, tc := range []struct {
		name string
		emit func(string)
	}{
		{"Info", l.Info},
		{"Warn", l.Warn},
		{"Error", l.Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			tc.emit(forgedEntry)

			// log.Println adds the single trailing newline that terminates the
			// record. Anything beyond that came from the message.
			if n := strings.Count(buf.String(), "\n"); n != 1 {
				t.Errorf("%s emitted %d lines, want 1:\n%q", tc.name, n, buf.String())
			}
		})
	}
}

// TestSafeMessage_LeavesOrdinaryTextAlone guards the other direction. Escaping
// that mangled normal messages would be paid for on every line the system
// writes, to defend against a case that arrives on almost none of them.
func TestSafeMessage_LeavesOrdinaryTextAlone(t *testing.T) {
	for _, msg := range []string{
		"listening on 127.0.0.1:8080 (drain 5s)",
		`audit {"action":"connection.created","workspace":"ws_0b1f"}`,
		"identity management enabled (admin client=lw-admin, base=https://kc.example/admin)",
		"email=user+tag@example.com",
	} {
		if got := safeMessage(msg); got != msg {
			t.Errorf("safeMessage altered an ordinary message:\n got %q\nwant %q", got, msg)
		}
	}
}
