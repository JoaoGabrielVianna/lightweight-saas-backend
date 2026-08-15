package config

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The configuration drift gate.
//
// [TD-004] is not "four variables were missing from compose". It is that four
// places describe the same set — the loader, `.env.example`, the compose file
// and the deployment guide — and three of them are updated by remembering to.
// The failure mode is silent: the deployment boots, reports healthy, and has a
// feature off because the container never received the variable that turns it
// on. Nobody finds out until someone asks why the console will not load.
//
// These tests close each direction of the drift:
//
//	code → table     a variable LoadConfig reads with no contract entry
//	table → code     a contract entry nothing reads (a stale row)
//	table → env      a variable an operator cannot discover from .env.example
//	table → compose  a variable the reference deployment never passes through
//
// They read the real files. That is the point: a test that checked the table
// against itself would pass forever.
//
// [TD-004]: docs/TECH_DEBT.md#td-004

const (
	envExamplePath = "../../.env.example"
	composePath    = "../../docker-compose.yml"
	loaderPath     = "config.go"
)

// envReadPattern matches the variable name in every form LoadConfig uses to
// read one: getEnv("X", …), possibly wrapped in parseBool / parseIntDefault /
// parseFloatDefault / parseCSV. Matching the inner call rather than the
// wrappers means a new wrapper cannot smuggle a variable past this test.
var envReadPattern = regexp.MustCompile(`getEnv\("([A-Z][A-Z0-9_]*)"`)

// varsReadByLoader returns every variable name config.go actually reads.
func varsReadByLoader(t *testing.T) []string {
	t.Helper()

	src, err := os.ReadFile(loaderPath)
	if err != nil {
		t.Fatalf("read %s: %v", loaderPath, err)
	}

	seen := map[string]bool{}
	for _, m := range envReadPattern.FindAllStringSubmatch(string(src), -1) {
		seen[m[1]] = true
	}
	if len(seen) == 0 {
		t.Fatal("found no getEnv calls in config.go; the pattern is wrong and this gate is asleep")
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestContract_CoversEveryVariableTheLoaderReads — the code → table direction.
//
// This is the one that catches the actual TD-004 mechanism: someone adds a
// feature, reads a new variable, and every downstream file stays as it was.
func TestContract_CoversEveryVariableTheLoaderReads(t *testing.T) {
	for _, name := range varsReadByLoader(t) {
		s, ok := SettingByName(name)
		if !ok {
			t.Errorf("config.go reads %s but it has no entry in the contract.\n"+
				"  Add one to settings in contract.go. Until you do, nothing makes it reach\n"+
				"  a container or appear in .env.example, and an operator can only find it\n"+
				"  by reading the source.", name)
			continue
		}
		if s.Consumer != ConsumerProcess {
			t.Errorf("config.go reads %s but the contract says it is consumed by %q",
				name, s.Consumer)
		}
	}
}

// TestContract_HasNoStaleProcessEntries — the table → code direction.
//
// A row for a variable nothing reads is worse than no row: it tells an operator
// to set something that does nothing, and it keeps a removed feature looking
// alive in the generated documentation.
func TestContract_HasNoStaleProcessEntries(t *testing.T) {
	read := map[string]bool{}
	for _, name := range varsReadByLoader(t) {
		read[name] = true
	}

	for _, s := range SettingsFor(ConsumerProcess) {
		if !read[s.Name] {
			t.Errorf("the contract declares %s as read by the process, but config.go does not read it.\n"+
				"  Either the loader lost it or the row is stale — both mislead whoever is "+
				"filling in a .env.", s.Name)
		}
	}
}

// envExampleVars returns the variables .env.example assigns, ignoring comments.
func envExampleVars(t *testing.T) map[string]bool {
	t.Helper()

	raw, err := os.ReadFile(envExamplePath)
	if err != nil {
		t.Fatalf("read %s: %v", envExamplePath, err)
	}

	out := map[string]bool{}
	assign := regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=`)
	for _, line := range strings.Split(string(raw), "\n") {
		if m := assign.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no assignments from .env.example")
	}
	return out
}

// TestContract_EveryVariableIsInEnvExample.
//
// `.env.example` is the file an operator copies. A variable absent from it is a
// variable that can only be discovered by reading Go source, which is the exact
// experience this slice exists to remove — including the optional ones, because
// "this knob exists and here is its default" is what stops someone rebuilding
// it as a fork.
func TestContract_EveryVariableIsInEnvExample(t *testing.T) {
	present := envExampleVars(t)

	for _, s := range Settings() {
		if !present[s.Name] {
			t.Errorf("%s is in the contract but not in .env.example (%s, %s).\n"+
				"  An operator copying .env.example cannot discover it.",
				s.Name, s.Consumer, s.Requirement)
		}
	}
}

// TestContract_EnvExampleHasNoUndeclaredVariables — the reverse.
//
// A variable in .env.example that nothing consumes is cargo: someone will set
// it carefully, and it will do nothing.
func TestContract_EnvExampleHasNoUndeclaredVariables(t *testing.T) {
	for name := range envExampleVars(t) {
		if _, ok := SettingByName(name); !ok {
			t.Errorf(".env.example sets %s but nothing declares it in the contract.\n"+
				"  Either it is dead and should go, or it is real and needs a row.", name)
		}
	}
}

// keyMaterial matches a long, unbroken base64-ish blob: the shape of a
// generated key, and nothing an operator would type as a placeholder.
//
// Deliberately narrow. The other secrets in this template are development
// passwords ("postgres", "admin") and a connection string containing one —
// obvious placeholders whose whole purpose is to make `docker compose up` work
// on a laptop. Flagging those would train whoever reads this failure to ignore
// it, and then it stops catching the case it exists for: someone runs
// `openssl rand -base64 32`, pastes the output into `.env.example` instead of
// `.env`, and commits a live key.
var keyMaterial = regexp.MustCompile(`^[A-Za-z0-9+/_-]{40,}={0,2}$`)

// TestContract_EnvExampleShipsNoGeneratedKey — `.env.example` is the file people
// copy AND the file people paste into issues.
func TestContract_EnvExampleShipsNoGeneratedKey(t *testing.T) {
	raw, err := os.ReadFile(envExamplePath)
	if err != nil {
		t.Fatalf("read %s: %v", envExamplePath, err)
	}

	assign := regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)
	for _, line := range strings.Split(string(raw), "\n") {
		m := assign.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		name, value := m[1], strings.TrimSpace(m[2])
		if !IsSecret(name) || value == "" {
			continue
		}
		if keyMaterial.MatchString(value) {
			t.Errorf("%s in .env.example holds a %d-character value shaped like a generated key.\n"+
				"  Ship an empty value: whoever copies this file must generate their own.",
				name, len(value))
		}
	}
}

// composeAPIEnvVars returns the variable names the compose `api` service passes
// into the container.
//
// Parsed with a scanner rather than a YAML library: adding a YAML dependency to
// the production module for one test would be a worse trade than a
// twenty-line parser over a file whose shape this repository controls. The
// parser fails loudly if the shape changes, which is the behaviour that matters.
func composeAPIEnvVars(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for name := range composeAPIEnv(t) {
		out[name] = true
	}
	return out
}

// composeAPIEnv returns the api service's environment block as name -> the
// literal YAML value, so a caller can ask not just whether a variable is
// passed but what it is passed AS.
func composeAPIEnv(t *testing.T) map[string]string {
	t.Helper()

	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read %s: %v", composePath, err)
	}
	lines := strings.Split(string(raw), "\n")

	inAPI, inEnv := false, false
	out := map[string]string{}
	entry := regexp.MustCompile(`^ {6}([A-Z][A-Z0-9_]*):[ \t]*(.*)$`)

	for _, line := range lines {
		switch {
		// A service key at two-space indentation ends the previous service.
		case regexp.MustCompile(`^  [a-z][a-z0-9-]*:`).MatchString(line):
			inAPI = strings.TrimSpace(line) == "api:"
			inEnv = false
			continue
		case !inAPI:
			continue
		// A four-space key inside the api service.
		case regexp.MustCompile(`^    [a-z_]+:`).MatchString(line):
			inEnv = strings.TrimSpace(line) == "environment:"
			continue
		}
		if inEnv {
			if m := entry.FindStringSubmatch(line); m != nil {
				out[m[1]] = strings.TrimSpace(m[2])
			}
		}
	}

	if len(out) == 0 {
		t.Fatal("parsed no environment entries from the compose api service — " +
			"the file's shape changed and this gate is asleep")
	}
	return out
}

// TestContract_ReferenceComposePassesEveryProcessVariable is the TD-004 gate
// proper.
//
// Every variable the process reads has to be passed through by the reference
// deployment. Docker does NOT forward the host environment into a container:
// a variable in `.env` that compose does not name in the service's
// `environment:` block simply never arrives, and the process falls back to its
// default with no warning anywhere.
//
// That is why the check is "named in the api service" and not "present in
// .env.example". The two failures look identical from inside the container and
// only one of them is visible in the file an operator edits.
func TestContract_ReferenceComposePassesEveryProcessVariable(t *testing.T) {
	passed := composeAPIEnvVars(t)

	var missing []string
	for _, s := range SettingsFor(ConsumerProcess) {
		if !passed[s.Name] {
			missing = append(missing, s.Name)
		}
	}
	sort.Strings(missing)

	for _, name := range missing {
		s, _ := SettingByName(name)
		t.Errorf("docker-compose.yml's api service never passes %s (%s).\n"+
			"  Setting it in .env has NO effect: compose does not forward the host\n"+
			"  environment. %s", name, s.Requirement, s.Purpose)
	}
}

// TestContract_ReferenceComposeHonoursWhatItPasses is the second half of the
// TD-004 gate, and it exists because passing the first one is not enough.
//
// The gate above proves compose NAMES every process variable. It does not prove
// compose passes the operator's VALUE. Three Keycloak variables were named and
// hardcoded:
//
//	KEYCLOAK_URL: http://localhost:${KC_HOST_PORT:-8081}
//	KEYCLOAK_JWKS_URL: http://keycloak:8080/realms/${KEYCLOAK_REALM}/...
//	KEYCLOAK_ADMIN_BASE_URL: http://keycloak:8080
//
// So an operator pointing the installation at their own Keycloak edited .env,
// compose passed the bundled dev values regardless, and the API died fetching
// JWKS from `keycloak:8080` — a hostname that does not resolve unless the
// `dev-idp` profile is running, and that they never typed anywhere. The
// symptom named a host the operator had never heard of. The documented
// self-hosting path could not work at all.
//
// A variable is honoured when its value interpolates itself. Anything else is
// a value the operator cannot change, and has to be justified here by name.
func TestContract_ReferenceComposeHonoursWhatItPasses(t *testing.T) {
	// DB_URL is assembled from POSTGRES_USER/PASSWORD/DB and the compose
	// network's hostname, on purpose: the operator's own DB_URL says
	// `localhost`, which inside the container is the container. The three
	// parts it is built from are themselves passed through, so the value
	// remains the operator's.
	derived := map[string]string{
		"DB_URL": "assembled from POSTGRES_USER/PASSWORD/DB against the compose `postgres` service",
	}

	for name, value := range composeAPIEnv(t) {
		if _, ok := derived[name]; ok {
			continue
		}
		if s, known := SettingByName(name); !known || s.Consumer != ConsumerProcess {
			continue
		}
		if strings.Contains(value, "${"+name+"}") || strings.Contains(value, "${"+name+":-") {
			continue
		}
		t.Errorf("docker-compose.yml passes %s as %q, which does not interpolate %s.\n"+
			"  Whatever the operator sets in .env is discarded, silently. Pass it as\n"+
			"  ${%s} or ${%s:-<default>}, or add it to the `derived` allowlist here\n"+
			"  with the reason.", name, value, name, name, name)
	}
}

// TestContract_RequiredVarsAreDerivedNotRepeated — Validate must read the table.
//
// The previous Validate had its required list written out by hand, which is a
// second place to forget. This asserts the two agree, so adding a required
// variable to the contract is enough to make the boot enforce it.
func TestContract_RequiredVarsAreDerivedNotRepeated(t *testing.T) {
	required := RequiredProcessVars()
	if len(required) == 0 {
		t.Fatal("no required process variables; the check would pass vacuously")
	}

	// Every required variable must actually be rejected when empty.
	for _, name := range required {
		cfg := validConfigFixture()
		clearField(t, cfg, name)

		problems := cfg.validationProblems()
		if !mentions(problems, name) {
			t.Errorf("%s is declared required but a config missing it passes validation.\n"+
				"  Problems reported: %v", name, problems)
		}
	}
}

func mentions(problems []string, name string) bool {
	for _, p := range problems {
		if strings.Contains(p, name) {
			return true
		}
	}
	return false
}

// TestContract_TableRendersForDocumentation — the published matrix is generated
// from this table, so a row with no purpose would ship an empty documentation
// cell.
func TestContract_TableRendersForDocumentation(t *testing.T) {
	for _, s := range Settings() {
		if s.Purpose == "" {
			t.Errorf("%s has no Purpose; it would render as a blank cell in the published matrix", s.Name)
		}
		if s.Consumer == "" || s.Requirement == "" {
			t.Errorf("%s is missing Consumer or Requirement", s.Name)
		}
	}

	rendered := RenderContractTable()
	for _, s := range Settings() {
		if !strings.Contains(rendered, "`"+s.Name+"`") {
			t.Errorf("%s is missing from the rendered table", s.Name)
		}
	}
}
