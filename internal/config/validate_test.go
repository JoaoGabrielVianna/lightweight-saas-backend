package config

import (
	"strings"
	"testing"
)

// Fail-fast validation.
//
// Every rule here exists because the alternative is a process that starts,
// reports healthy, and is wrong in a way nobody finds until it matters. The
// tests are on validationProblems rather than on Validate so they assert the
// decision without a subprocess; Validate's only added behaviour is to print
// the list and exit, which TestValidate_ExitsOnBadConfig covers end to end.

// validConfigFixture is the minimum coherent configuration: everything required
// present and well-formed, everything optional absent.
//
// Tests start from it and break ONE thing, so a failure names the rule that
// fired rather than "the fixture is wrong".
func validConfigFixture() *Config {
	return &Config{
		Port:                   "8080",
		DBUrl:                  "postgres://saas:saas@localhost:5432/saas?sslmode=disable",
		KeycloakURL:            "https://auth.example.com",
		KeycloakRealm:          "saas",
		KeycloakClientID:       "saas-console",
		KeycloakJWKSURL:        "https://auth.example.com/realms/saas/protocol/openid-connect/certs",
		ShutdownTimeoutSeconds: DefaultShutdownTimeoutSeconds,
	}
}

// clearField empties the field a required variable maps to.
//
// Named explicitly rather than reflected: the mapping from variable to field is
// the thing under test in TestContract_RequiredVarsAreDerivedNotRepeated, and
// deriving it by reflection would make that test assert reflection works.
func clearField(t *testing.T, c *Config, name string) {
	t.Helper()
	switch name {
	case "DB_URL":
		c.DBUrl = ""
	case "KEYCLOAK_URL":
		c.KeycloakURL = ""
	case "KEYCLOAK_REALM":
		c.KeycloakRealm = ""
	case "KEYCLOAK_CLIENT_ID":
		c.KeycloakClientID = ""
	default:
		t.Fatalf("clearField does not know how to clear %s — it was added to the contract "+
			"as required without a case here", name)
	}
}

func TestValidate_AcceptsACoherentConfiguration(t *testing.T) {
	if problems := validConfigFixture().validationProblems(); len(problems) != 0 {
		t.Errorf("a valid configuration was rejected: %v", problems)
	}
}

// TestValidate_ReportsEveryProblemAtOnce.
//
// One-at-a-time reporting turns configuring a fresh deployment into a sequence
// of restarts, each revealing the next thing wrong. That is exactly the
// trial-and-error experience this slice exists to remove, so the count matters
// as much as the content.
func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	cfg := &Config{
		Port:                   "not-a-port",
		DBUrl:                  "mysql://nope/db",
		KeycloakURL:            "auth.example.com", // no scheme
		KeycloakRealm:          "",
		KeycloakClientID:       "",
		KeycloakJWKSURL:        "",
		SecretsMasterKey:       "not base64 at all !!!",
		ShutdownTimeoutSeconds: 0,
	}

	problems := cfg.validationProblems()
	if len(problems) < 6 {
		t.Fatalf("only %d problems reported for a configuration with at least 6:\n%s",
			len(problems), strings.Join(problems, "\n"))
	}

	for _, want := range []string{
		"PORT", "DB_URL", "KEYCLOAK_URL", "KEYCLOAK_REALM",
		"KEYCLOAK_CLIENT_ID", "KEYCLOAK_JWKS_URL", "SECRETS_MASTER_KEY",
		"SHUTDOWN_TIMEOUT_SECONDS",
	} {
		if !mentions(problems, want) {
			t.Errorf("no problem mentions %s:\n%s", want, strings.Join(problems, "\n"))
		}
	}
}

func TestValidate_URLs(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantVar string
	}{
		{"keycloak url without a scheme", func(c *Config) { c.KeycloakURL = "auth.example.com" }, "KEYCLOAK_URL"},
		{"keycloak url with a bad scheme", func(c *Config) { c.KeycloakURL = "ftp://auth.example.com" }, "KEYCLOAK_URL"},
		{"jwks url without a host", func(c *Config) { c.KeycloakJWKSURL = "https:///certs" }, "KEYCLOAK_JWKS_URL"},
		{"admin base url without a scheme", func(c *Config) { c.KeycloakAdminBaseURL = "keycloak:8080" }, "KEYCLOAK_ADMIN_BASE_URL"},
		{"db url with the wrong driver", func(c *Config) { c.DBUrl = "mysql://u:p@h:3306/db" }, "DB_URL"},
		{"db url naming no database", func(c *Config) { c.DBUrl = "postgres://u:p@host:5432" }, "DB_URL"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigFixture()
			tc.mutate(cfg)
			problems := cfg.validationProblems()
			if !mentions(problems, tc.wantVar) {
				t.Errorf("no problem mentions %s: %v", tc.wantVar, problems)
			}
		})
	}
}

// TestValidate_DBURLProblemNeverEchoesTheValue — DB_URL carries the database
// password, and a validation error is the most likely thing to be pasted into
// an issue.
func TestValidate_DBURLProblemNeverEchoesTheValue(t *testing.T) {
	const password = "sup3r-s3cret-pw"
	cfg := validConfigFixture()
	cfg.DBUrl = "mysql://user:" + password + "@host:3306/db"

	for _, p := range cfg.validationProblems() {
		if strings.Contains(p, password) {
			t.Errorf("a validation problem echoed the database password: %q", p)
		}
	}
}

// TestValidate_MasterKey — the two failures are distinguished because the fixes
// differ, and neither message may contain key material.
func TestValidate_MasterKey(t *testing.T) {
	// 32 bytes, base64. The value this test asserts is ACCEPTED, so it is a
	// throwaway constant rather than anything real.
	const good = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

	t.Run("absent is fine — it omits connections", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.SecretsMasterKey = ""
		if problems := cfg.validationProblems(); mentions(problems, "SECRETS_MASTER_KEY") {
			t.Errorf("an absent master key was reported as a problem: %v", problems)
		}
	})

	t.Run("a correct key is accepted", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.SecretsMasterKey = good
		if problems := cfg.validationProblems(); mentions(problems, "SECRETS_MASTER_KEY") {
			t.Errorf("a valid master key was rejected: %v", problems)
		}
	})

	t.Run("wrong length says how many bytes it got", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.SecretsMasterKey = "c2hvcnQ=" // "short", 5 bytes
		problems := cfg.validationProblems()
		if !mentions(problems, "SECRETS_MASTER_KEY") {
			t.Fatalf("a short master key was accepted: %v", problems)
		}
		if !mentions(problems, "5 bytes") {
			t.Errorf("the message does not say how long the key actually was: %v", problems)
		}
	})

	t.Run("no message ever contains the key", func(t *testing.T) {
		for _, key := range []string{"not-base64-!!!", "c2hvcnQ=", good + "extra"} {
			cfg := validConfigFixture()
			cfg.SecretsMasterKey = key
			for _, p := range cfg.validationProblems() {
				if strings.Contains(p, key) {
					t.Errorf("a validation problem echoed the master key: %q", p)
				}
			}
		}
	})
}

func TestValidate_ShutdownTimeout(t *testing.T) {
	t.Run("zero is refused", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.ShutdownTimeoutSeconds = 0
		if !mentions(cfg.validationProblems(), "SHUTDOWN_TIMEOUT_SECONDS") {
			t.Error("a zero shutdown timeout was accepted; that is an unbounded drain")
		}
	})

	t.Run("absurdly long is refused", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.ShutdownTimeoutSeconds = 3600
		if !mentions(cfg.validationProblems(), "SHUTDOWN_TIMEOUT_SECONDS") {
			t.Error("an hour-long shutdown timeout was accepted; the platform would SIGKILL first")
		}
	})

	t.Run("the accessor falls back rather than returning zero", func(t *testing.T) {
		cfg := &Config{}
		if got := cfg.ShutdownTimeout().Seconds(); got != DefaultShutdownTimeoutSeconds {
			t.Errorf("ShutdownTimeout() = %vs on a zero config, want the default %ds",
				got, DefaultShutdownTimeoutSeconds)
		}
	})
}

// TestValidate_RateLimitsCannotBeSwitchedOff — 0 means "the default" by
// documented contract, but a negative value is someone trying to disable the
// limiter, and that must not read as "use the default" silently.
func TestValidate_RateLimitsCannotBeSwitchedOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Config)
		wantVar string
	}{
		{"edge", func(c *Config) { c.RateLimitEdgeRPS = -1 }, "RATE_LIMIT_EDGE_RPS"},
		{"credential", func(c *Config) { c.RateLimitCredentialRPS = -1 }, "RATE_LIMIT_CREDENTIAL_RPS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigFixture()
			tc.mutate(cfg)
			if !mentions(cfg.validationProblems(), tc.wantVar) {
				t.Errorf("a negative %s was accepted", tc.wantVar)
			}
		})
	}

	t.Run("zero is accepted and means the default", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.RateLimitEdgeRPS = 0
		cfg.RateLimitCredentialRPS = 0
		if problems := cfg.validationProblems(); len(problems) != 0 {
			t.Errorf("zero rate limits were rejected: %v", problems)
		}
	})
}

func TestValidate_CORSOrigins(t *testing.T) {
	cases := []struct {
		name     string
		origins  []string
		rejected bool
	}{
		{"a normal origin", []string{"https://console.example.com"}, false},
		{"localhost with a port", []string{"http://localhost:5174"}, false},
		{"several", []string{"https://a.example.com", "https://b.example.com"}, false},
		{"a wildcard", []string{"*"}, true},
		{"a trailing slash", []string{"https://console.example.com/"}, true},
		{"a path", []string{"https://console.example.com/app"}, true},
		{"no scheme", []string{"console.example.com"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigFixture()
			cfg.CORSAllowedOrigins = tc.origins
			got := mentions(cfg.validationProblems(), "CORS_ALLOWED_ORIGINS")
			if got != tc.rejected {
				t.Errorf("rejected = %v, want %v (origins %v)", got, tc.rejected, tc.origins)
			}
		})
	}
}

// TestValidate_ImpossibleCombinations — configurations where each value is fine
// on its own and the pair cannot be honoured. These are the ones a per-field
// check can never catch.
func TestValidate_ImpossibleCombinations(t *testing.T) {
	t.Run("half-configured admin client", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.KeycloakAdminClientID = "saas-admin"
		// secret deliberately absent
		problems := cfg.validationProblems()
		if !mentions(problems, "KEYCLOAK_ADMIN_CLIENT_SECRET") {
			t.Errorf("a half-configured admin client was accepted; /admin would be "+
				"silently absent: %v", problems)
		}
	})

	t.Run("both set is fine", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.KeycloakAdminClientID = "saas-admin"
		cfg.KeycloakAdminClientSecret = "s"
		if problems := cfg.validationProblems(); len(problems) != 0 {
			t.Errorf("a fully configured admin client was rejected: %v", problems)
		}
	})

	t.Run("console enabled with no client id and no playground", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.AdminConsoleEnabled = true
		problems := cfg.validationProblems()
		if !mentions(problems, "ADMIN_CONSOLE_CLIENT_ID") {
			t.Errorf("the console was allowed to fall back to the development client "+
				"in a deployment with the playground off: %v", problems)
		}
	})

	t.Run("console enabled with an explicit client id is fine", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.AdminConsoleEnabled = true
		cfg.AdminConsoleClientID_ = "saas-console"
		if problems := cfg.validationProblems(); len(problems) != 0 {
			t.Errorf("a correctly configured console was rejected: %v", problems)
		}
	})
}

// TestValidate_MalformedValuesFromTheLoaderSurface — the strict parsers record
// problems, and Validate has to report them. Without this the recording would
// be dead code and a mistyped value would still be silently defaulted.
func TestValidate_MalformedValuesFromTheLoaderSurface(t *testing.T) {
	cfg := validConfigFixture()
	cfg.malformed = []string{"RATE_LIMIT_EDGE_RPS must be a number (got \"2O\")"}

	problems := cfg.validationProblems()
	if !mentions(problems, "RATE_LIMIT_EDGE_RPS") {
		t.Errorf("a value the loader could not parse did not reach validation: %v", problems)
	}
}
