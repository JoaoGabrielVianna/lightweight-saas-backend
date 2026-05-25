package config

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// keycloakEnvKeys is the set of env vars that LoadConfig reads. Tests
// snapshot these via withEnv so a test can mutate the process environment
// without leaking changes into sibling tests.
var keycloakEnvKeys = []string{
	"PORT",
	"DB_URL",
	"GIN_LOG_ENABLED",
	"GIN_ACCESS_LOG_ENABLED",
	"KEYCLOAK_URL",
	"KEYCLOAK_REALM",
	"KEYCLOAK_CLIENT_ID",
	"KEYCLOAK_CLIENT_SECRET",
	"KEYCLOAK_JWKS_URL",
	"KEYCLOAK_ALLOWED_CLIENT_IDS",
	"KEYCLOAK_ADMIN_CLIENT_ID",
	"KEYCLOAK_ADMIN_CLIENT_SECRET",
	"KEYCLOAK_ADMIN_BASE_URL",
	"DEV_PLAYGROUND_ENABLED",
	"DEV_PLAYGROUND_CLIENT_ID",
	"ADMIN_LIVE_CHECK_TTL_SECONDS",
}

// snapshotEnv records the current value (and presence) of every env key
// LoadConfig consults, returning a restore func.
func snapshotEnv(t *testing.T) func() {
	t.Helper()
	type entry struct {
		val   string
		isSet bool
	}
	saved := make(map[string]entry, len(keycloakEnvKeys))
	for _, k := range keycloakEnvKeys {
		v, ok := os.LookupEnv(k)
		saved[k] = entry{val: v, isSet: ok}
		os.Unsetenv(k)
	}
	return func() {
		for k, e := range saved {
			if e.isSet {
				os.Setenv(k, e.val)
			} else {
				os.Unsetenv(k)
			}
		}
	}
}

// withMinimalRequiredEnv sets the env vars LoadConfig.Validate requires so
// the caller can layer additional vars on top without tripping the fatal
// missing-vars exit.
func withMinimalRequiredEnv(t *testing.T) {
	t.Helper()
	os.Setenv("DB_URL", "postgres://u:p@h:5432/db")
	os.Setenv("KEYCLOAK_URL", "http://kc.local")
	os.Setenv("KEYCLOAK_REALM", "saas")
	os.Setenv("KEYCLOAK_CLIENT_ID", "saas-api")
	// JWKS will be derived from URL+Realm.
}

// chdirTemp moves the process into a temp directory so godotenv.Load() can't
// find a .env file from the test working directory. Returns nothing — the
// testing framework restores cwd via t.Chdir.
func chdirTemp(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func TestGetEnv(t *testing.T) {
	t.Run("returns value when set", func(t *testing.T) {
		const key = "TEST_GETENV_KEY_PRESENT"
		t.Setenv(key, "hello")
		if got := getEnv(key, "fallback"); got != "hello" {
			t.Fatalf("getEnv = %q, want %q", got, "hello")
		}
	})
	t.Run("returns empty value when set to empty", func(t *testing.T) {
		const key = "TEST_GETENV_KEY_EMPTY"
		t.Setenv(key, "")
		if got := getEnv(key, "fallback"); got != "" {
			t.Fatalf("getEnv = %q, want empty (env var set to empty must beat fallback)", got)
		}
	})
	t.Run("returns fallback when unset", func(t *testing.T) {
		// Use a key very unlikely to be present in any environment.
		os.Unsetenv("TEST_GETENV_KEY_ABSENT")
		if got := getEnv("TEST_GETENV_KEY_ABSENT", "fallback"); got != "fallback" {
			t.Fatalf("getEnv = %q, want %q", got, "fallback")
		}
	})
}

func TestParseBool(t *testing.T) {
	truthy := []string{"true", "1", "yes", "on"}
	falsy := []string{"false", "0", "no", "off", "", "True", "TRUE", "YES", "garbage"}
	for _, v := range truthy {
		if !parseBool(v) {
			t.Errorf("parseBool(%q) = false, want true", v)
		}
	}
	for _, v := range falsy {
		if parseBool(v) {
			t.Errorf("parseBool(%q) = true, want false (case-sensitive contract)", v)
		}
	}
}

func TestParseIntDefault(t *testing.T) {
	cases := []struct {
		in, want, fallback int
		raw                string
	}{
		{raw: "", want: 7, fallback: 7},
		{raw: " 42 ", want: 42, fallback: 7},
		{raw: "0", want: 0, fallback: 7},
		{raw: "-3", want: -3, fallback: 7},
		{raw: "abc", want: 7, fallback: 7},
		{raw: "12abc", want: 7, fallback: 7},
	}
	for _, tc := range cases {
		if got := parseIntDefault(tc.raw, tc.fallback); got != tc.want {
			t.Errorf("parseIntDefault(%q, %d) = %d, want %d", tc.raw, tc.fallback, got, tc.want)
		}
	}
}

func TestParseCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: ",", want: nil},
		{in: "  ", want: nil},
		{in: "a", want: []string{"a"}},
		{in: "a,b,c", want: []string{"a", "b", "c"}},
		{in: "a, b ,, c", want: []string{"a", "b", "c"}},
		{in: " , ,trailing", want: []string{"trailing"}},
	}
	for _, tc := range cases {
		got := parseCSV(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseCSV(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestAdminLiveCheckTTL(t *testing.T) {
	cases := []struct {
		seconds int
		want    time.Duration
	}{
		{seconds: 0, want: 30 * time.Second},
		{seconds: -5, want: 30 * time.Second},
		{seconds: 60, want: 60 * time.Second},
		{seconds: 1, want: 1 * time.Second},
	}
	for _, tc := range cases {
		c := &Config{AdminLiveCheckTTLSeconds: tc.seconds}
		if got := c.AdminLiveCheckTTL(); got != tc.want {
			t.Errorf("AdminLiveCheckTTL(%d) = %s, want %s", tc.seconds, got, tc.want)
		}
	}
}

func TestApplyGinConfig(t *testing.T) {
	// ApplyGinConfig mutates package-global state in gin (mode, default
	// writer). Snapshot and restore so we don't poison sibling tests.
	prevMode := gin.Mode()
	prevWriter := gin.DefaultWriter
	t.Cleanup(func() {
		gin.SetMode(prevMode)
		gin.DefaultWriter = prevWriter
	})

	t.Run("logs enabled keeps debug mode and writer", func(t *testing.T) {
		gin.SetMode(gin.DebugMode)
		gin.DefaultWriter = io.Discard
		stdout := os.Stdout
		gin.DefaultWriter = stdout
		c := &Config{GinLogEnabled: true, GinAccessLogEnabled: true}
		c.ApplyGinConfig()
		if gin.Mode() != gin.DebugMode {
			t.Errorf("gin.Mode = %q, want %q (logs enabled shouldn't flip to release)", gin.Mode(), gin.DebugMode)
		}
		if gin.DefaultWriter != stdout {
			t.Error("gin.DefaultWriter was changed when AccessLogEnabled is true")
		}
	})

	t.Run("disabling gin logs sets release mode", func(t *testing.T) {
		gin.SetMode(gin.DebugMode)
		c := &Config{GinLogEnabled: false, GinAccessLogEnabled: true}
		c.ApplyGinConfig()
		if gin.Mode() != gin.ReleaseMode {
			t.Errorf("gin.Mode = %q, want %q", gin.Mode(), gin.ReleaseMode)
		}
	})

	t.Run("disabling access logs discards writer", func(t *testing.T) {
		gin.SetMode(gin.DebugMode)
		buf := &bytes.Buffer{}
		gin.DefaultWriter = buf
		c := &Config{GinLogEnabled: true, GinAccessLogEnabled: false}
		c.ApplyGinConfig()
		if gin.DefaultWriter != io.Discard {
			t.Error("gin.DefaultWriter not redirected to io.Discard when access logs disabled")
		}
	})
}

func TestLoadConfig_Defaults(t *testing.T) {
	restore := snapshotEnv(t)
	t.Cleanup(restore)
	chdirTemp(t)

	withMinimalRequiredEnv(t)

	cfg := LoadConfig()

	if cfg.Port != "8080" {
		t.Errorf("Port default = %q, want 8080", cfg.Port)
	}
	if !cfg.GinLogEnabled {
		t.Error("GinLogEnabled default should be true")
	}
	if !cfg.GinAccessLogEnabled {
		t.Error("GinAccessLogEnabled default should be true")
	}
	if cfg.DevPlaygroundEnabled {
		t.Error("DevPlaygroundEnabled default should be false")
	}
	if cfg.DevPlaygroundClientID != "saas-dev-playground" {
		t.Errorf("DevPlaygroundClientID default = %q, want saas-dev-playground", cfg.DevPlaygroundClientID)
	}
	if cfg.AdminLiveCheckTTLSeconds != 0 {
		t.Errorf("AdminLiveCheckTTLSeconds default = %d, want 0", cfg.AdminLiveCheckTTLSeconds)
	}
	// JWKS URL should be derived when not explicitly set.
	wantJWKS := "http://kc.local/realms/saas/protocol/openid-connect/certs"
	if cfg.KeycloakJWKSURL != wantJWKS {
		t.Errorf("derived KeycloakJWKSURL = %q, want %q", cfg.KeycloakJWKSURL, wantJWKS)
	}
}

func TestLoadConfig_DerivedJWKS_StripsTrailingSlash(t *testing.T) {
	restore := snapshotEnv(t)
	t.Cleanup(restore)
	chdirTemp(t)

	withMinimalRequiredEnv(t)
	os.Setenv("KEYCLOAK_URL", "http://kc.local/")

	cfg := LoadConfig()
	wantJWKS := "http://kc.local/realms/saas/protocol/openid-connect/certs"
	if cfg.KeycloakJWKSURL != wantJWKS {
		t.Errorf("derived KeycloakJWKSURL = %q, want %q (trailing slash must be stripped)", cfg.KeycloakJWKSURL, wantJWKS)
	}
}

func TestLoadConfig_ExplicitJWKS_NotOverwritten(t *testing.T) {
	restore := snapshotEnv(t)
	t.Cleanup(restore)
	chdirTemp(t)

	withMinimalRequiredEnv(t)
	os.Setenv("KEYCLOAK_JWKS_URL", "http://override/jwks")

	cfg := LoadConfig()
	if cfg.KeycloakJWKSURL != "http://override/jwks" {
		t.Errorf("explicit KeycloakJWKSURL = %q, want http://override/jwks", cfg.KeycloakJWKSURL)
	}
}

func TestLoadConfig_OverridesAndParses(t *testing.T) {
	restore := snapshotEnv(t)
	t.Cleanup(restore)
	chdirTemp(t)

	withMinimalRequiredEnv(t)
	os.Setenv("PORT", "9000")
	os.Setenv("GIN_LOG_ENABLED", "false")
	os.Setenv("GIN_ACCESS_LOG_ENABLED", "no")
	os.Setenv("KEYCLOAK_ALLOWED_CLIENT_IDS", "a, b ,, c")
	os.Setenv("DEV_PLAYGROUND_ENABLED", "1")
	os.Setenv("DEV_PLAYGROUND_CLIENT_ID", "custom-dev")
	os.Setenv("ADMIN_LIVE_CHECK_TTL_SECONDS", "120")
	os.Setenv("KEYCLOAK_ADMIN_CLIENT_ID", "kc-admin")
	os.Setenv("KEYCLOAK_ADMIN_CLIENT_SECRET", "shh")
	os.Setenv("KEYCLOAK_ADMIN_BASE_URL", "http://kc:8080")

	// Snapshot gin globals around LoadConfig (Validate path doesn't touch
	// gin, but we keep this defensive in case future changes call
	// ApplyGinConfig from LoadConfig).
	prevMode := gin.Mode()
	prevWriter := gin.DefaultWriter
	t.Cleanup(func() {
		gin.SetMode(prevMode)
		gin.DefaultWriter = prevWriter
	})

	cfg := LoadConfig()

	if cfg.Port != "9000" {
		t.Errorf("Port = %q, want 9000", cfg.Port)
	}
	if cfg.GinLogEnabled {
		t.Error("GinLogEnabled should parse false")
	}
	if cfg.GinAccessLogEnabled {
		t.Error("GinAccessLogEnabled should parse no→false")
	}
	wantAllowed := []string{"a", "b", "c"}
	if !reflect.DeepEqual(cfg.KeycloakAllowedClientIDs, wantAllowed) {
		t.Errorf("KeycloakAllowedClientIDs = %#v, want %#v", cfg.KeycloakAllowedClientIDs, wantAllowed)
	}
	if !cfg.DevPlaygroundEnabled {
		t.Error("DevPlaygroundEnabled should parse 1→true")
	}
	if cfg.DevPlaygroundClientID != "custom-dev" {
		t.Errorf("DevPlaygroundClientID = %q, want custom-dev", cfg.DevPlaygroundClientID)
	}
	if cfg.AdminLiveCheckTTLSeconds != 120 {
		t.Errorf("AdminLiveCheckTTLSeconds = %d, want 120", cfg.AdminLiveCheckTTLSeconds)
	}
	if cfg.AdminLiveCheckTTL() != 120*time.Second {
		t.Errorf("AdminLiveCheckTTL() = %s, want 2m", cfg.AdminLiveCheckTTL())
	}
	if cfg.KeycloakAdminClientID != "kc-admin" || cfg.KeycloakAdminClientSecret != "shh" {
		t.Errorf("admin creds didn't roundtrip: id=%q secret=%q", cfg.KeycloakAdminClientID, cfg.KeycloakAdminClientSecret)
	}
	if cfg.KeycloakAdminBaseURL != "http://kc:8080" {
		t.Errorf("KeycloakAdminBaseURL = %q", cfg.KeycloakAdminBaseURL)
	}
}

func TestLoadConfig_LoadsDotEnvFromCwd(t *testing.T) {
	restore := snapshotEnv(t)
	t.Cleanup(restore)

	dir := t.TempDir()
	t.Chdir(dir)
	// godotenv.Load does NOT overwrite already-set env vars, so we must
	// leave PORT unset in the process env for the .env value to win.
	envFile := filepath.Join(dir, ".env")
	contents := strings.Join([]string{
		"PORT=7777",
		"DB_URL=postgres://u:p@h:5432/db",
		"KEYCLOAK_URL=http://kc.local",
		"KEYCLOAK_REALM=saas",
		"KEYCLOAK_CLIENT_ID=saas-api",
	}, "\n") + "\n"
	if err := os.WriteFile(envFile, []byte(contents), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg := LoadConfig()
	if cfg.Port != "7777" {
		t.Errorf("Port = %q, want 7777 (loaded from .env)", cfg.Port)
	}
	if cfg.DBUrl == "" {
		t.Error("DBUrl should have been populated from .env")
	}
}

// TestLoadConfig_FatalOnMissingRequired exercises the log.Fatal exit branch
// in Validate by re-running this binary as a subprocess with a flag that
// triggers LoadConfig under cleared env. We assert the child exits non-zero
// and that the stderr/stdout mentions every missing key.
func TestLoadConfig_FatalOnMissingRequired(t *testing.T) {
	if os.Getenv("LSB_CONFIG_FATAL_CHILD") == "1" {
		// Child invocation: clear required vars and run LoadConfig. The
		// expected outcome is os.Exit(1) inside logger.Fatal.
		for _, k := range keycloakEnvKeys {
			os.Unsetenv(k)
		}
		// Move to a temp dir so a stray .env in the developer's checkout
		// doesn't accidentally satisfy validation.
		dir, err := os.MkdirTemp("", "lsb-cfg-fatal-")
		if err != nil {
			os.Exit(99)
		}
		defer os.RemoveAll(dir)
		if err := os.Chdir(dir); err != nil {
			os.Exit(99)
		}
		LoadConfig()
		// Should never reach here.
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLoadConfig_FatalOnMissingRequired")
	cmd.Env = append(os.Environ(), "LSB_CONFIG_FATAL_CHILD=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		t.Fatalf("child exited 0; expected non-zero. output:\n%s", out.String())
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 0 {
		t.Fatalf("child exited 0 unexpectedly. output:\n%s", out.String())
	}
	// Validate the missing-vars message names every required key. The
	// logger writes through the standard log package which is captured by
	// the test binary's stdout.
	output := out.String()
	for _, want := range []string{"DB_URL", "KEYCLOAK_URL", "KEYCLOAK_REALM", "KEYCLOAK_CLIENT_ID", "KEYCLOAK_JWKS_URL"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing-vars message did not mention %q. output:\n%s", want, output)
		}
	}
}
