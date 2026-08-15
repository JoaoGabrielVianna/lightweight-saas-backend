package database

import (
	"bytes"
	"strings"
	"testing"
)

// runCLI is a test harness over RunCLI that captures both streams.
func runCLI(args []string, dsn string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = RunCLI(args, dsn, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestRunCLI_UsageAndArgumentErrors covers every path that can be exercised
// without a database. These are the ones a developer hits by mistake, so the
// messages are part of the contract.
func TestRunCLI_UsageAndArgumentErrors(t *testing.T) {
	// A DSN that parses but points nowhere; the argument checks these cases
	// exercise all return before any connection is attempted.
	const unusedDSN = "postgres://nobody@192.0.2.1:1/nodb"

	tests := []struct {
		name     string
		args     []string
		dsn      string
		wantCode int
		wantErr  string // substring expected on stderr
		wantOut  string // substring expected on stdout
	}{
		{
			name:     "no arguments prints usage",
			args:     nil,
			dsn:      unusedDSN,
			wantCode: 2,
			wantErr:  "Usage:",
		},
		{
			name:     "help goes to stdout and succeeds",
			args:     []string{"help"},
			dsn:      "",
			wantCode: 0,
			wantOut:  "Commands:",
		},
		{
			name:     "--help is accepted too",
			args:     []string{"--help"},
			dsn:      "",
			wantCode: 0,
			wantOut:  "force <version>",
		},
		{
			name:     "unknown command names itself",
			args:     []string{"sideways"},
			dsn:      unusedDSN,
			wantCode: 2,
			wantErr:  `unknown command "sideways"`,
		},
		{
			name:     "missing DB_URL is reported before anything is attempted",
			args:     []string{"up"},
			dsn:      "",
			wantCode: 2,
			wantErr:  "DB_URL is not set",
		},
		{
			name:     "steps without a count",
			args:     []string{"steps"},
			dsn:      unusedDSN,
			wantCode: 2,
			wantErr:  "steps requires a count",
		},
		{
			name:     "steps with a non-integer count",
			args:     []string{"steps", "one"},
			dsn:      unusedDSN,
			wantCode: 2,
			wantErr:  `"one" is not an integer`,
		},
		{
			name:     "steps 0 is refused rather than silently doing nothing",
			args:     []string{"steps", "0"},
			dsn:      unusedDSN,
			wantCode: 2,
			wantErr:  "would do nothing",
		},
		{
			name:     "force without a version",
			args:     []string{"force"},
			dsn:      unusedDSN,
			wantCode: 2,
			wantErr:  "force requires a version",
		},
		{
			name:     "force with a non-integer version",
			args:     []string{"force", "latest"},
			dsn:      unusedDSN,
			wantCode: 2,
			wantErr:  `"latest" is not an integer`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(tc.args, tc.dsn)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stdout=%q stderr=%q)",
					code, tc.wantCode, stdout, stderr)
			}
			if tc.wantErr != "" && !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantErr)
			}
			if tc.wantOut != "" && !strings.Contains(stdout, tc.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, tc.wantOut)
			}
		})
	}
}

// TestRunCLI_ReportsMigrationFailures covers each command's execution path.
// Every one must exit 1 — not 0, and not 2 — when the migration itself cannot
// run, because a deploy script that treats "database unreachable" as success is
// how a release ships against an unmigrated schema.
func TestRunCLI_ReportsMigrationFailures(t *testing.T) {
	t.Parallel()

	commands := [][]string{
		{"up"},
		{"down"},
		{"steps", "1"},
		{"force", "1"},
		{"version"},
	}

	for _, args := range commands {
		t.Run(args[0], func(t *testing.T) {
			t.Parallel()
			code, _, stderr := runCLI(args, unreachableDSN)
			if code != 1 {
				t.Errorf("exit code = %d, want 1 (stderr=%q)", code, stderr)
			}
			if !strings.Contains(stderr, "error:") {
				t.Errorf("stderr = %q, want it to report the error", stderr)
			}
		})
	}
}

// TestRunCLI_HelpDocumentsEveryCommand keeps the usage text honest: a command
// that RunCLI accepts but usage never mentions is undiscoverable.
func TestRunCLI_HelpDocumentsEveryCommand(t *testing.T) {
	_, stdout, _ := runCLI([]string{"help"}, "")
	for _, cmd := range []string{"up", "down", "steps", "version", "force"} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("usage text does not document the %q command", cmd)
		}
	}
}

// TestReport_ExitCodes pins the code/stream contract the Makefile targets and
// any CI wrapper rely on: 1 means the migration failed, 0 means it ran.
func TestReport_ExitCodes(t *testing.T) {
	var buf bytes.Buffer
	if code := report(&buf, nil); code != 0 {
		t.Errorf("report(nil) = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "ok") {
		t.Errorf("successful report did not print ok: %q", buf.String())
	}

	buf.Reset()
	if code := report(&buf, errFailedProbe); code != 1 {
		t.Errorf("report(err) = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "failed probe") {
		t.Errorf("failing report did not print the error: %q", buf.String())
	}
}

// errFailedProbe is a stand-in error for the report() contract test.
var errFailedProbe = errStub("failed probe")

type errStub string

func (e errStub) Error() string { return string(e) }
