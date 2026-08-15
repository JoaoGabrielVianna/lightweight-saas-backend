package connection

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"gorm.io/gorm"
)

// The `secrets` command: inspect and rotate the master keys that seal provider
// credentials.
//
// Shaped after internal/database.RunCLI, for the same reason it exists there:
// the logic belongs with the domain that owns the rows, and cmd/secrets stays a
// six-line main that loads .env and forwards os.Args. That keeps the behaviour
// testable without spawning a process.
//
// # Exit codes are the interface
//
//	0  success, including "nothing needed rotating"
//	1  the operation ran and something is wrong: a row failed to rotate, or a
//	   persisted key version is not configured
//	2  the invocation or the configuration was wrong; nothing was attempted
//
// A deploy script must be able to branch on rotation having finished without
// parsing prose, and an operator must be able to tell "I typed the command
// wrong" from "the installation has a problem". Those are the three answers.
//
// # What this never prints
//
// Key material, plaintext credentials, ciphertext, nonces. What it does print
// is version numbers, counts, and connection PUBLIC ids — the same identifiers
// that appear in every API response and every log line already.

const secretsUsage = `Usage: secrets <command> [flags]

Inspect and rotate the master keys sealing provider credentials at rest.

Commands:
  status              Report which key versions persisted secrets use, and
                      which configured keys are safe to remove.
  rotate              Re-seal every persisted secret under the current key.
                      Idempotent: rows already current are left untouched.
  rotate --dry-run    Report what rotate would do. Reads metadata only; it
                      decrypts nothing, so it cannot detect wrong key material.
  help                This message.

Environment:
  DB_URL              PostgreSQL DSN.
  SECRETS_KEYRING     1:<base64>,2:<base64> — every key this process can
                      decrypt with.
  SECRETS_KEY_CURRENT Which of those versions seals new secrets. Optional with
                      a single key.
  SECRETS_MASTER_KEY  Legacy single key; equivalent to SECRETS_KEYRING=1:<key>.

Exit codes:
  0  success (including nothing to do)
  1  a row failed to rotate, or a persisted key version is not configured
  2  bad invocation or bad configuration
`

// SecretsCLIDeps is what RunSecretsCLI needs from its caller.
//
// The database handle and the keyring arrive already built, so this function
// contains no environment reading and no connection logic — which is what lets
// a test drive every command against a real schema without setting a variable.
type SecretsCLIDeps struct {
	DB      *gorm.DB
	Keyring *secrets.Keyring

	// Configured is false when no keyring is configured at all. Distinguished
	// from a nil Keyring caused by a parse failure so the message can say which
	// of the two an operator is looking at.
	Configured bool

	// ConfigError, when non-nil, is why the keyring could not be built. Safe to
	// print: config never puts key material in an error.
	ConfigError error
}

// RunSecretsCLI executes one `secrets` command and returns the process exit
// code.
func RunSecretsCLI(ctx context.Context, args []string, deps SecretsCLIDeps, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(errOut, secretsUsage)
		return 2
	}

	command := args[0]
	if command == "help" || command == "-h" || command == "--help" {
		fmt.Fprint(out, secretsUsage)
		return 0
	}

	switch command {
	case "status", "rotate":
	default:
		fmt.Fprintf(errOut, "unknown command %q\n\n", command)
		fmt.Fprint(errOut, secretsUsage)
		return 2
	}

	dryRun := false
	for _, flag := range args[1:] {
		switch flag {
		case "--dry-run":
			if command != "rotate" {
				fmt.Fprintf(errOut, "--dry-run applies to `rotate`, not to %q\n", command)
				return 2
			}
			dryRun = true
		default:
			fmt.Fprintf(errOut, "unknown flag %q\n\n", flag)
			fmt.Fprint(errOut, secretsUsage)
			return 2
		}
	}

	if deps.ConfigError != nil {
		fmt.Fprintf(errOut, "configuration: %v\n", deps.ConfigError)
		return 2
	}
	if !deps.Configured || deps.Keyring == nil {
		fmt.Fprintln(errOut, "no secret keyring configured — set SECRETS_KEYRING "+
			"(or the legacy SECRETS_MASTER_KEY). Without one there are no sealed "+
			"credentials to rotate.")
		return 2
	}
	if deps.DB == nil {
		fmt.Fprintln(errOut, "DB_URL is not set")
		return 2
	}

	rotator := NewRotator(deps.DB, deps.Keyring)
	if rotator == nil {
		fmt.Fprintln(errOut, "cannot build a rotator from the given database and keyring")
		return 2
	}

	switch {
	case command == "status":
		return secretsStatus(ctx, rotator, out, errOut)
	case dryRun:
		return secretsDryRun(ctx, rotator, out, errOut)
	default:
		return secretsRotate(ctx, rotator, out, errOut)
	}
}

// secretsStatus answers the two questions an operator has around a rotation:
// has it finished, and is the old key safe to destroy.
func secretsStatus(ctx context.Context, r *Rotator, out, errOut io.Writer) int {
	census, err := r.Census(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
	return RenderStatus(census, r.Keyring(), out, errOut)
}

// RenderStatus writes the status report and returns the exit code.
//
// Split from the query so the OUTPUT — which is the part that must never carry
// key material, and the part an operator reads before destroying a key — is
// testable without a database. Exported for the same reason: the test that
// proves it prints no secrets should not need PostgreSQL to run.
func RenderStatus(census KeyVersionCensus, ring *secrets.Keyring, out, errOut io.Writer) int {
	fmt.Fprintf(out, "Keyring versions:  %s\n", versionsWithCurrent(ring))
	fmt.Fprintf(out, "Current key:       v%d\n\n", ring.CurrentVersion())

	fmt.Fprintln(out, "Persisted connection secrets by key version:")
	if census.Total() == 0 {
		fmt.Fprintln(out, "  (none)")
	}
	for _, v := range census.Versions() {
		note := ""
		switch {
		case v == ring.CurrentVersion():
			note = "  ← current"
		case !ring.Has(v):
			note = "  ← NOT CONFIGURED: these connections cannot be opened"
		}
		fmt.Fprintf(out, "  v%-4d %6d%s\n", v, census.Rows[v], note)
	}

	missing := census.Unopenable(ring)
	safe := census.SafeToRemove(ring)

	fmt.Fprintln(out)
	if len(safe) == 0 {
		fmt.Fprintln(out, "Safe to remove:    (none)")
	} else {
		fmt.Fprintf(out, "Safe to remove:    %s\n", joinVersions(safe))
		fmt.Fprintln(out, "                   No persisted secret needs these. Removing them from")
		fmt.Fprintln(out, "                   SECRETS_KEYRING is safe — AFTER they are out of your backups'")
		fmt.Fprintln(out, "                   recovery window, since an older dump may still need them.")
	}

	if len(missing) > 0 {
		fmt.Fprintln(errOut)
		fmt.Fprintf(errOut, "PROBLEM: %d key version(s) used by persisted secrets are not configured: %s\n",
			len(missing), joinVersions(missing))
		fmt.Fprintln(errOut, "Those connections cannot be opened, so the workspaces routing through them")
		fmt.Fprintln(errOut, "will answer credentials_unavailable. Restore the key material into")
		fmt.Fprintln(errOut, "SECRETS_KEYRING and re-run `secrets rotate`.")
		return 1
	}
	return 0
}

// secretsDryRun reports what rotation would do, from metadata alone.
func secretsDryRun(ctx context.Context, r *Rotator, out, errOut io.Writer) int {
	plan, err := r.Plan(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
	return RenderPlan(plan, out)
}

// RenderPlan writes the dry-run report and returns the exit code.
func RenderPlan(plan RotationPlan, out io.Writer) int {
	fmt.Fprintf(out, "Dry run. Nothing was decrypted and nothing was written.\n\n")
	fmt.Fprintf(out, "Current key:       v%d\n", plan.Current)
	fmt.Fprintf(out, "Total secrets:     %d\n", plan.Census.Total())
	fmt.Fprintf(out, "Already current:   %d\n", plan.AlreadyCurrent)
	fmt.Fprintf(out, "Would rotate:      %d\n", plan.NeedRotation-plan.Blocked)

	if plan.Blocked > 0 {
		fmt.Fprintf(out, "Blocked:           %d (key versions %s are not configured)\n",
			plan.Blocked, joinVersions(plan.MissingVersions))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "A dry run reads the stored key VERSION of each row and nothing else. It")
	fmt.Fprintln(out, "cannot tell you that the key configured for a version is the right one —")
	fmt.Fprintln(out, "proving that means running AES-GCM over the row, which is the real run.")

	if plan.Blocked > 0 {
		return 1
	}
	return 0
}

// secretsRotate performs the rotation and reports it.
func secretsRotate(ctx context.Context, r *Rotator, out, errOut io.Writer) int {
	report, err := r.Rotate(ctx)
	return RenderRotation(report, r.CurrentVersion(), err, out, errOut)
}

// RenderRotation writes the rotation report and returns the exit code.
//
// runErr is the run-level error — an interrupted context, a failed listing — as
// distinct from the per-row failures inside the report. Both produce a non-zero
// exit, and both print the report first: the rows that committed stayed
// committed, and an operator who pressed Ctrl-C needs to know how far it got.
func RenderRotation(report RotationReport, current int, runErr error, out, errOut io.Writer) int {
	fmt.Fprintf(out, "Current key:       v%d\n", current)
	fmt.Fprintf(out, "total:             %d\n", report.Total)
	fmt.Fprintf(out, "already_current:   %d\n", report.AlreadyCurrent)
	fmt.Fprintf(out, "rotated:           %d\n", report.Rotated)
	fmt.Fprintf(out, "failed:            %d\n", report.Failed())

	if len(report.Failures) > 0 {
		fmt.Fprintln(errOut)
		fmt.Fprintln(errOut, "These connections were NOT rotated and still hold their previous ciphertext:")
		for _, f := range report.Failures {
			fmt.Fprintf(errOut, "  %s\n", f)
		}
		fmt.Fprintln(errOut)
		fmt.Fprintln(errOut, "  missing_key_version — the key for that version is not in SECRETS_KEYRING.")
		fmt.Fprintln(errOut, "                        Restore it and re-run; rotated rows are skipped.")
		fmt.Fprintln(errOut, "  cannot_open         — the configured key does not open the row. Wrong key")
		fmt.Fprintln(errOut, "                        material for that version, or the row was altered.")
	}

	if runErr != nil {
		fmt.Fprintf(errOut, "\nrun did not finish: %v\n", runErr)
		fmt.Fprintln(errOut, "Rows already rotated are committed. Re-run to resume.")
		return 1
	}
	if !report.Complete() {
		return 1
	}
	return 0
}

// versionsWithCurrent renders the keyring for an operator.
func versionsWithCurrent(ring *secrets.Keyring) string {
	versions := ring.Versions()
	parts := make([]string, 0, len(versions))
	for _, v := range versions {
		label := "v" + strconv.Itoa(v)
		if v == ring.CurrentVersion() {
			label += " (current)"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func joinVersions(versions []int) string {
	sorted := append([]int(nil), versions...)
	sort.Ints(sorted)
	parts := make([]string, 0, len(sorted))
	for _, v := range sorted {
		parts = append(parts, "v"+strconv.Itoa(v))
	}
	return strings.Join(parts, ", ")
}
