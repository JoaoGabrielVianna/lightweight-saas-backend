package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validConfigJSON is the minimal JSON document that passes both the schema
// and the semantic Validate. Used across most tests as a baseline; tests
// mutate a copy when they want to exercise a failure path.
const validConfigJSON = `{
  "project":  {"name": "demo", "environment": "local"},
  "auth":     {
    "provider": "keycloak",
    "realm":    "demo",
    "client":   {"id": "demo-api"},
    "roles":    ["admin", "user"],
    "admin":    {"username": "admin", "email": "a@b.test"}
  },
  "ports":    {"api": 8080, "postgres": 5432, "keycloak": 8081, "keycloak_postgres": 5433},
  "features": {"seed_users": true},
  "seed_users": [{"username": "u", "email": "u@b.test", "roles": ["user"]}]
}`

func writeTempJSON(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "project.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp json: %v", err)
	}
	return p
}

func TestLoad_Valid(t *testing.T) {
	cfg, err := Load(writeTempJSON(t, validConfigJSON))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if cfg.Project.Name != "demo" {
		t.Errorf("name: %q", cfg.Project.Name)
	}
	if cfg.Auth.Client.ID != "demo-api" {
		t.Errorf("client.id: %q", cfg.Auth.Client.ID)
	}
	if !cfg.Features["seed_users"] {
		t.Errorf("feature flag lost")
	}
}

func TestLoad_RejectsUnknownEnvironment(t *testing.T) {
	bad := strings.Replace(validConfigJSON, `"environment": "local"`, `"environment": "qa"`, 1)
	_, err := Load(writeTempJSON(t, bad))
	if err == nil {
		t.Fatalf("expected schema error for environment=qa")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("expected schema failure, got %v", err)
	}
}

func TestLoad_RejectsExtraTopLevelField(t *testing.T) {
	bad := strings.Replace(validConfigJSON, `"project":`, `"unknown_field": 1, "project":`, 1)
	_, err := Load(writeTempJSON(t, bad))
	if err == nil {
		t.Fatalf("expected schema error for additionalProperties")
	}
}

func TestLoad_RejectsMissingClientID(t *testing.T) {
	bad := strings.Replace(validConfigJSON, `"client":   {"id": "demo-api"}`, `"client":   {}`, 1)
	_, err := Load(writeTempJSON(t, bad))
	if err == nil {
		t.Fatalf("expected schema error for missing client.id")
	}
}

func TestLoad_RejectsBadEmail(t *testing.T) {
	bad := strings.Replace(validConfigJSON, `"a@b.test"`, `"not-an-email"`, 1)
	_, err := Load(writeTempJSON(t, bad))
	if err == nil {
		t.Fatalf("expected schema error for bad email format")
	}
}

func TestLoad_RejectsRoleDuplicates(t *testing.T) {
	bad := strings.Replace(validConfigJSON, `["admin", "user"]`, `["admin", "admin"]`, 1)
	_, err := Load(writeTempJSON(t, bad))
	if err == nil {
		t.Fatalf("expected schema error for duplicate roles")
	}
}

func TestLoad_RejectsPortOutOfRange(t *testing.T) {
	bad := strings.Replace(validConfigJSON, `"api": 8080`, `"api": 70000`, 1)
	_, err := Load(writeTempJSON(t, bad))
	if err == nil {
		t.Fatalf("expected schema error for port > 65535")
	}
}

func TestSave_OmitsSecretFields(t *testing.T) {
	cfg, err := Load(writeTempJSON(t, validConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Inject secrets in memory — they must never reach disk.
	cfg.Auth.Client.Secret = "must-not-persist"
	cfg.Auth.Admin.Password = "also-must-not-persist"
	cfg.SeedUsers[0].Password = "neither"

	path := filepath.Join(t.TempDir(), "out.json")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	for _, leak := range []string{"must-not-persist", "also-must-not-persist", "neither"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("Save leaked %q to disk:\n%s", leak, raw)
		}
	}
}

func TestSave_RoundTrip(t *testing.T) {
	cfg, err := Load(writeTempJSON(t, validConfigJSON))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := Save(out, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg2, err := Load(out)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Compare via JSON to ignore field order / map iteration.
	a, _ := json.Marshal(cfg)
	b, _ := json.Marshal(cfg2)
	if string(a) != string(b) {
		t.Errorf("round-trip mismatch:\na=%s\nb=%s", a, b)
	}
}

func TestValidate_OnlyKeycloakSupported(t *testing.T) {
	cfg := &ProjectConfig{
		Project:  Project{Name: "x", Environment: "local"},
		Auth:     Auth{Provider: "auth0", Realm: "r", Client: Client{ID: "c"}, Roles: []string{"user"}},
		Ports:    Ports{API: 1, Postgres: 1, Keycloak: 1},
		Features: map[string]bool{},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "keycloak") {
		t.Errorf("expected keycloak-only error, got %v", err)
	}
}
