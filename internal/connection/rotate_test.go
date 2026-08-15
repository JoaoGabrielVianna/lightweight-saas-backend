package connection

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
)

// The census arithmetic, without a database.
//
// These are the functions an operator's decision to DESTROY a key rests on, so
// they are worth pinning independently of the query that feeds them. The query
// itself is covered against real PostgreSQL in rotate_integration_test.go.

func unitRing(t *testing.T, current int, versions ...int) *secrets.Keyring {
	t.Helper()
	materials := make([]secrets.KeyMaterial, 0, len(versions))
	for _, v := range versions {
		materials = append(materials, secrets.KeyMaterial{
			Version: v, Key: bytes.Repeat([]byte{byte(v)}, secrets.KeySize),
		})
	}
	ring, err := secrets.NewKeyring(materials, current)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	return ring
}

func TestCensus_TotalAndVersions(t *testing.T) {
	c := KeyVersionCensus{Rows: map[int]int64{3: 5, 1: 2, 2: 0}}

	if got := c.Total(); got != 7 {
		t.Errorf("Total = %d, want 7", got)
	}
	got := c.Versions()
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("Versions = %v, want ascending [1 2 3]", got)
	}
}

func TestCensus_EmptyInstallation(t *testing.T) {
	c := KeyVersionCensus{Rows: map[int]int64{}}
	ring := unitRing(t, 1, 1)

	if c.Total() != 0 {
		t.Errorf("Total = %d, want 0", c.Total())
	}
	if got := c.Unopenable(ring); len(got) != 0 {
		t.Errorf("Unopenable = %v on an empty installation", got)
	}
	// The current key is never removable, however empty the table: the next
	// connection anyone creates will be sealed with it.
	if got := c.SafeToRemove(ring); len(got) != 0 {
		t.Errorf("SafeToRemove = %v, and one of them is the CURRENT key", got)
	}
}

// TestCensus_SafeToRemoveNeverIncludesTheCurrentKey, stated on its own because
// getting it wrong destroys an installation rather than degrading it.
func TestCensus_SafeToRemoveNeverIncludesTheCurrentKey(t *testing.T) {
	ring := unitRing(t, 3, 1, 2, 3)
	c := KeyVersionCensus{Rows: map[int]int64{}} // nothing stored at all

	for _, v := range c.SafeToRemove(ring) {
		if v == ring.CurrentVersion() {
			t.Fatal("the current key was reported safe to remove")
		}
	}
	got := c.SafeToRemove(ring)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("SafeToRemove = %v, want [1 2]", got)
	}
}

func TestCensus_AKeyStillInUseIsNotSafeToRemove(t *testing.T) {
	ring := unitRing(t, 2, 1, 2)
	c := KeyVersionCensus{Rows: map[int]int64{1: 1, 2: 40}}

	if got := c.SafeToRemove(ring); len(got) != 0 {
		t.Errorf("SafeToRemove = %v with one row still on v1 — destroying it "+
			"would make that connection unrecoverable", got)
	}
}

func TestCensus_ReportsVersionsTheRingCannotOpen(t *testing.T) {
	ring := unitRing(t, 2, 2)
	c := KeyVersionCensus{Rows: map[int]int64{1: 3, 2: 9, 5: 1}}

	got := c.Unopenable(ring)
	if len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Errorf("Unopenable = %v, want ascending [1 5]", got)
	}
}

// ---------------------------------------------------------------------------
// Report shape
// ---------------------------------------------------------------------------

func TestRotationReport_CompleteAndFailed(t *testing.T) {
	clean := RotationReport{Total: 3, AlreadyCurrent: 1, Rotated: 2}
	if !clean.Complete() || clean.Failed() != 0 {
		t.Errorf("a report with no failures is not complete: %+v", clean)
	}

	broken := RotationReport{Total: 3, Rotated: 2, Failures: []RotationFailure{
		{ConnectionPublicID: "conn_x", KeyVersion: 1, Reason: "missing_key_version"},
	}}
	if broken.Complete() || broken.Failed() != 1 {
		t.Errorf("a report with a failure claims completion: %+v", broken)
	}
}

// TestRotationFailure_StringIsSafeToPrint. This line goes to an operator's
// terminal and into deploy logs.
func TestRotationFailure_StringIsSafeToPrint(t *testing.T) {
	f := RotationFailure{
		ConnectionPublicID: "conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		KeyVersion:         1,
		Reason:             "missing_key_version",
	}
	got := f.String()

	for _, want := range []string{"conn_3f2504e0", "v1", "missing_key_version"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not contain %q", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// TestNewRotator_RefusesAMissingCollaborator. Same "this is not wired" signal
// the domains use, and here it also means a rotation can never run against a
// keyring nobody configured.
func TestNewRotator_RefusesAMissingCollaborator(t *testing.T) {
	if NewRotator(nil, unitRing(t, 1, 1)) != nil {
		t.Error("a rotator was built with no database")
	}
	if NewRotator(nil, nil) != nil {
		t.Error("a rotator was built with nothing at all")
	}
}

// ---------------------------------------------------------------------------
// CLI invocation handling
// ---------------------------------------------------------------------------

// TestSecretsCLI_RefusesBeforeTouchingAnything covers the paths that must not
// reach a database: usage errors and configuration errors.
func TestSecretsCLI_RefusesBeforeTouchingAnything(t *testing.T) {
	run := func(deps SecretsCLIDeps, args ...string) (int, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := RunSecretsCLI(context.Background(), args, deps, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	t.Run("help exits 0 and goes to stdout", func(t *testing.T) {
		code, out, errOut := run(SecretsCLIDeps{}, "help")
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if !strings.Contains(out, "secrets <command>") {
			t.Errorf("help did not reach stdout:\n%s", out)
		}
		if errOut != "" {
			t.Errorf("help wrote to stderr: %q", errOut)
		}
	})

	t.Run("usage goes to stderr on a bad invocation", func(t *testing.T) {
		code, out, errOut := run(SecretsCLIDeps{})
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if out != "" {
			t.Errorf("a usage error wrote to stdout: %q", out)
		}
		if !strings.Contains(errOut, "Usage") {
			t.Errorf("no usage on stderr:\n%s", errOut)
		}
	})

	t.Run("a configuration error is reported, not a database error", func(t *testing.T) {
		code, _, errOut := run(SecretsCLIDeps{
			ConfigError: errors.New("SECRETS_KEYRING version 2: the key is not valid base64"),
		}, "status")
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if !strings.Contains(errOut, "SECRETS_KEYRING") {
			t.Errorf("the configuration problem was not surfaced:\n%s", errOut)
		}
	})

	t.Run("no keyring configured says so", func(t *testing.T) {
		code, _, errOut := run(SecretsCLIDeps{}, "rotate")
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if !strings.Contains(errOut, "SECRETS_KEYRING") {
			t.Errorf("the message does not say what to set:\n%s", errOut)
		}
	})

	t.Run("a keyring but no database", func(t *testing.T) {
		code, _, errOut := run(SecretsCLIDeps{
			Keyring: unitRing(t, 1, 1), Configured: true,
		}, "status")
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if !strings.Contains(errOut, "DB_URL") {
			t.Errorf("the message does not name DB_URL:\n%s", errOut)
		}
	})
}

// TestSecretsCLI_UsageNeverPrintsAKey — the usage text names the variables and
// must not carry an example that looks like real material.
func TestSecretsCLI_UsageNeverPrintsAKey(t *testing.T) {
	var out bytes.Buffer
	RunSecretsCLI(context.Background(), []string{"help"}, SecretsCLIDeps{}, &out, &bytes.Buffer{})

	text := out.String()
	for _, want := range []string{"SECRETS_KEYRING", "SECRETS_KEY_CURRENT", "SECRETS_MASTER_KEY"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage does not mention %s", want)
		}
	}
	// The placeholder must stay a placeholder.
	if strings.Contains(text, "=\n") || strings.Contains(text, "QUFB") {
		t.Error("the usage text contains something shaped like base64 key material")
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------
//
// The output an operator reads, tested without a database. Two properties
// matter here and neither needs PostgreSQL: the exit code has to be right, and
// nothing printed may be derived from a key or a plaintext.

func TestRenderStatus_ReportsWhatIsSafeToRemove(t *testing.T) {
	ring := unitRing(t, 2, 1, 2)

	t.Run("rotation still in progress", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RenderStatus(KeyVersionCensus{Rows: map[int]int64{1: 4, 2: 10}}, ring, &out, &errOut)
		if code != 0 {
			t.Errorf("exit code = %d, want 0 — every version is configured", code)
		}
		text := out.String()
		if !strings.Contains(text, "Safe to remove:    (none)") {
			t.Errorf("v1 still has rows but was not withheld:\n%s", text)
		}
		if !strings.Contains(text, "v2") || !strings.Contains(text, "current") {
			t.Errorf("the current key is not identified:\n%s", text)
		}
	})

	t.Run("rotation complete", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RenderStatus(KeyVersionCensus{Rows: map[int]int64{2: 14}}, ring, &out, &errOut)
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		text := out.String()
		if !strings.Contains(text, "Safe to remove:") || !strings.Contains(text, "v1") {
			t.Errorf("v1 was not reported removable:\n%s", text)
		}
		// The advice that separates "no live row needs it" from "no BACKUP
		// needs it" — an older dump restored later still requires the old key.
		if !strings.Contains(text, "backups") {
			t.Errorf("the backup caveat is missing; an operator could destroy a key an "+
				"older dump still needs:\n%s", text)
		}
	})

	t.Run("a persisted version is not configured", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RenderStatus(KeyVersionCensus{Rows: map[int]int64{1: 2, 2: 5}},
			unitRing(t, 2, 2), &out, &errOut)
		if code != 1 {
			t.Errorf("exit code = %d, want 1 — this is an actionable defect", code)
		}
		if !strings.Contains(errOut.String(), "NOT configured") &&
			!strings.Contains(out.String(), "NOT CONFIGURED") {
			t.Errorf("the missing version was not called out:\nstdout:\n%s\nstderr:\n%s",
				out.String(), errOut.String())
		}
	})

	t.Run("an empty installation", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RenderStatus(KeyVersionCensus{Rows: map[int]int64{}}, ring, &out, &errOut)
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "(none)") {
			t.Errorf("an empty installation renders oddly:\n%s", out.String())
		}
	})
}

func TestRenderPlan_ReportsWithoutClaimingTooMuch(t *testing.T) {
	t.Run("everything is rotatable", func(t *testing.T) {
		plan := PlanFrom(KeyVersionCensus{Rows: map[int]int64{1: 3, 2: 7}}, unitRing(t, 2, 1, 2))
		var out bytes.Buffer
		if code := RenderPlan(plan, &out); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		text := out.String()
		if !strings.Contains(text, "Would rotate:      3") {
			t.Errorf("wrong rotate count:\n%s", text)
		}
		if !strings.Contains(text, "Already current:   7") {
			t.Errorf("wrong already-current count:\n%s", text)
		}
		// The honesty clause: a dry run cannot detect wrong key MATERIAL.
		if !strings.Contains(text, "cannot tell you") {
			t.Errorf("the dry run overclaims; it must state its own limit:\n%s", text)
		}
	})

	t.Run("some rows are blocked by a missing key", func(t *testing.T) {
		plan := PlanFrom(KeyVersionCensus{Rows: map[int]int64{1: 3, 2: 7}}, unitRing(t, 2, 2))
		var out bytes.Buffer
		if code := RenderPlan(plan, &out); code != 1 {
			t.Errorf("exit code = %d, want 1 — a blocked row cannot be rotated", code)
		}
		if !strings.Contains(out.String(), "Blocked:") {
			t.Errorf("blocked rows were not reported:\n%s", out.String())
		}
	})
}

func TestRenderRotation_ExitCodes(t *testing.T) {
	t.Run("a clean run exits 0", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RenderRotation(RotationReport{Total: 5, AlreadyCurrent: 2, Rotated: 3},
			2, nil, &out, &errOut)
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		for _, want := range []string{"total:             5", "rotated:           3", "failed:            0"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output does not contain %q:\n%s", want, out.String())
			}
		}
		if errOut.Len() != 0 {
			t.Errorf("a clean run wrote to stderr: %q", errOut.String())
		}
	})

	t.Run("nothing to do still exits 0", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := RenderRotation(RotationReport{}, 1, nil, &out, &errOut); code != 0 {
			t.Errorf("exit code = %d, want 0 — an idempotent rerun is a success", code)
		}
	})

	t.Run("a failed row exits 1 and is named", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RenderRotation(RotationReport{
			Total: 2, Rotated: 1,
			Failures: []RotationFailure{
				{ConnectionPublicID: "conn_abc", KeyVersion: 1, Reason: "missing_key_version"},
			},
		}, 2, nil, &out, &errOut)
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		text := errOut.String()
		if !strings.Contains(text, "conn_abc") || !strings.Contains(text, "missing_key_version") {
			t.Errorf("the failure is not diagnosable from the output:\n%s", text)
		}
		if !strings.Contains(text, "Re-run") && !strings.Contains(text, "re-run") {
			t.Errorf("the output does not say the run is resumable:\n%s", text)
		}
	})

	t.Run("an interrupted run exits 1 and says it is resumable", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RenderRotation(RotationReport{Total: 9, Rotated: 4}, 2,
			context.Canceled, &out, &errOut)
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(errOut.String(), "did not finish") {
			t.Errorf("an interruption was not reported:\n%s", errOut.String())
		}
		if !strings.Contains(out.String(), "rotated:           4") {
			t.Errorf("the partial progress was not reported:\n%s", out.String())
		}
	})
}

// TestRenderers_NeverPrintKeyMaterial. The keyring is passed in whole; the only
// thing that may come out of it is a version number.
func TestRenderers_NeverPrintKeyMaterial(t *testing.T) {
	ring := unitRing(t, 2, 1, 2)

	// The exact base64 an operator would have in SECRETS_KEYRING for this ring.
	forbidden := []string{
		secrets.EncodeKey(bytes.Repeat([]byte{1}, secrets.KeySize)),
		secrets.EncodeKey(bytes.Repeat([]byte{2}, secrets.KeySize)),
	}

	census := KeyVersionCensus{Rows: map[int]int64{1: 3, 2: 7}}
	surfaces := map[string]func() (string, string){
		"status": func() (string, string) {
			var out, errOut bytes.Buffer
			RenderStatus(census, ring, &out, &errOut)
			return out.String(), errOut.String()
		},
		"status with a missing key": func() (string, string) {
			var out, errOut bytes.Buffer
			RenderStatus(census, unitRing(t, 2, 2), &out, &errOut)
			return out.String(), errOut.String()
		},
		"dry run": func() (string, string) {
			var out bytes.Buffer
			RenderPlan(PlanFrom(census, ring), &out)
			return out.String(), ""
		},
		"rotation": func() (string, string) {
			var out, errOut bytes.Buffer
			RenderRotation(RotationReport{
				Total: 10, Rotated: 3, AlreadyCurrent: 7,
				Failures: []RotationFailure{
					{ConnectionPublicID: "conn_abc", KeyVersion: 1, Reason: "cannot_open"},
				},
			}, 2, errors.New("connection reset"), &out, &errOut)
			return out.String(), errOut.String()
		},
	}

	for name, render := range surfaces {
		stdout, stderr := render()
		for _, material := range forbidden {
			if strings.Contains(stdout, material) || strings.Contains(stderr, material) {
				t.Errorf("%s printed key material", name)
			}
			// A prefix would be almost as bad as the whole thing.
			if strings.Contains(stdout+stderr, material[:24]) {
				t.Errorf("%s printed a prefix of key material", name)
			}
		}
	}
}
