package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// freshConfig returns a ProjectConfig matching validConfigJSON for direct
// generator testing without round-tripping through disk.
func freshConfig() *ProjectConfig {
	return &ProjectConfig{
		Project:   Project{Name: "demo-app", Environment: "local"},
		Auth:      Auth{Provider: "keycloak", Realm: "demo", Client: Client{ID: "demo-api"}, Roles: []string{"admin", "user"}, Admin: Admin{Username: "admin", Email: "a@b.test"}},
		Ports:     Ports{API: 8080, Postgres: 5432, Keycloak: 8081, KeycloakPostgres: 5433},
		Features:  map[string]bool{"seed_users": true},
		SeedUsers: []SeedUser{{Username: "u1", Email: "u1@b.test", Roles: []string{"user"}}},
	}
}

func freshSecrets() Secrets {
	return Secrets{
		AdminPassword:     "supersecret-admin",
		ClientSecret:      "supersecret-client",
		SeedUserPassword:  "supersecret-seed",
		AdminClientSecret: "supersecret-admin-client",
	}
}

func TestGenerateAll_WritesAllOwnedFiles(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")

	if err := GenerateAll(root, freshConfig(), freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}

	for _, rel := range []string{".env", ".env.example", "config/project.schema.json", "deploy/keycloak/realm-export.json"} {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing %s: %v", rel, err)
		} else if info.Size() == 0 {
			t.Errorf("empty %s", rel)
		}
	}
}

func TestGenerateEnv_InjectsSecretsAndProjectValues(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	if err := GenerateAll(root, freshConfig(), freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	env, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	s := string(env)

	for _, must := range []string{
		"KEYCLOAK_ADMIN_PASSWORD=supersecret-admin",
		"KEYCLOAK_CLIENT_SECRET=supersecret-client",
		"SEED_USER_PASSWORD=supersecret-seed",
		"KEYCLOAK_REALM=demo",
		"KEYCLOAK_CLIENT_ID=demo-api",
		"POSTGRES_DB=demo_app_db",
		"PORT=8080",
	} {
		if !strings.Contains(s, must) {
			t.Errorf(".env missing line %q\nfile:\n%s", must, s)
		}
	}
}

func TestGenerateEnvExample_UsesDefaultSecrets(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	if err := GenerateAll(root, freshConfig(), freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	example, _ := os.ReadFile(filepath.Join(root, ".env.example"))
	// .env.example MUST NOT leak the real secrets from .env.
	for _, leak := range []string{"supersecret-admin", "supersecret-client", "supersecret-seed"} {
		if strings.Contains(string(example), leak) {
			t.Errorf(".env.example leaked %q from .env", leak)
		}
	}
}

func TestGenerateRealmExport_ShapeAndSecrets(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	if err := GenerateAll(root, freshConfig(), freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "deploy/keycloak/realm-export.json"))

	var realm map[string]any
	if err := json.Unmarshal(raw, &realm); err != nil {
		t.Fatalf("realm json invalid: %v\n%s", err, raw)
	}

	if realm["realm"] != "demo" {
		t.Errorf("realm name: %v", realm["realm"])
	}
	clients := realm["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("expected 1 client")
	}
	client := clients[0].(map[string]any)
	if client["clientId"] != "demo-api" {
		t.Errorf("clientId: %v", client["clientId"])
	}
	if client["secret"] != "supersecret-client" {
		t.Errorf("client secret not injected from Secrets, got: %v", client["secret"])
	}
	users := realm["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("expected 1 seeded user")
	}
	creds := users[0].(map[string]any)["credentials"].([]any)
	if creds[0].(map[string]any)["value"] != "supersecret-seed" {
		t.Errorf("seed user password not injected from Secrets")
	}
}

func TestGenerateRealmExport_SkipsUsersWhenFeatureOff(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	cfg := freshConfig()
	cfg.Features["seed_users"] = false

	if err := GenerateAll(root, cfg, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "deploy/keycloak/realm-export.json"))
	var realm map[string]any
	_ = json.Unmarshal(raw, &realm)
	if users, ok := realm["users"]; ok && users != nil {
		if u, _ := users.([]any); len(u) != 0 {
			t.Errorf("expected zero users with seed_users=false, got %v", users)
		}
	}
}

func TestGenerateRealmExport_EmitsDevPlaygroundClient_WhenFeatureOn(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	cfg := freshConfig()
	cfg.Features["dev_playground"] = true

	if err := GenerateAll(root, cfg, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "deploy/keycloak/realm-export.json"))
	var realm map[string]any
	if err := json.Unmarshal(raw, &realm); err != nil {
		t.Fatalf("parse realm: %v", err)
	}

	clients, _ := realm["clients"].([]any)
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients (api + dev playground), got %d", len(clients))
	}

	var play map[string]any
	for _, c := range clients {
		if m, _ := c.(map[string]any); m["clientId"] == DevPlaygroundClientID {
			play = m
		}
	}
	if play == nil {
		t.Fatalf("dev playground client missing (expected clientId=%q)", DevPlaygroundClientID)
	}

	// Critical attributes — if any of these flip wrong the PKCE flow breaks.
	if play["publicClient"] != true {
		t.Errorf("playground client must be public (publicClient=true)")
	}
	if play["standardFlowEnabled"] != true {
		t.Errorf("playground client must have standard flow enabled")
	}
	if play["directAccessGrantsEnabled"] != false {
		t.Errorf("playground client must NOT allow direct access grants")
	}
	if _, hasSecret := play["secret"]; hasSecret {
		t.Errorf("playground client must NOT carry a client secret")
	}
	attrs, _ := play["attributes"].(map[string]any)
	if attrs["pkce.code.challenge.method"] != "S256" {
		t.Errorf("playground client must enforce PKCE S256, got %v", attrs["pkce.code.challenge.method"])
	}
	redirects, _ := play["redirectUris"].([]any)
	if len(redirects) == 0 {
		t.Errorf("playground client needs at least one redirect URI")
	}
	// Both frontends (/dev/auth + /admin) share this PKCE client; ensure
	// each redirect target is registered.
	wantTargets := map[string]bool{"/dev/auth": false, "/admin": false}
	for _, r := range redirects {
		uri := r.(string)
		for target := range wantTargets {
			if strings.Contains(uri, target) {
				wantTargets[target] = true
			}
		}
	}
	for target, seen := range wantTargets {
		if !seen {
			t.Errorf("redirect URI for %q is missing from %v", target, redirects)
		}
	}
}

func TestGenerateRealmExport_OmitsDevPlaygroundClient_WhenFeatureOff(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	cfg := freshConfig()
	cfg.Features["dev_playground"] = false

	if err := GenerateAll(root, cfg, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "deploy/keycloak/realm-export.json"))
	var realm map[string]any
	_ = json.Unmarshal(raw, &realm)

	clients, _ := realm["clients"].([]any)
	for _, c := range clients {
		if m, _ := c.(map[string]any); m["clientId"] == DevPlaygroundClientID {
			t.Errorf("playground client must not be present when feature is off")
		}
	}
}

func TestAllowedClientIDs_AutoDerivesPrimaryOnly(t *testing.T) {
	cfg := freshConfig()
	cfg.Features["dev_playground"] = false
	got := allowedClientIDs(cfg)
	if len(got) != 1 || got[0] != cfg.Auth.Client.ID {
		t.Errorf("expected [%s], got %v", cfg.Auth.Client.ID, got)
	}
}

func TestAllowedClientIDs_AutoDerivesPlaygroundWhenFeatureOn(t *testing.T) {
	cfg := freshConfig()
	cfg.Features["dev_playground"] = true
	got := allowedClientIDs(cfg)
	want := []string{cfg.Auth.Client.ID, DevPlaygroundClientID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (primary must come first for stable ordering)", got, want)
	}
}

func TestAllowedClientIDs_ExplicitOverrideWins(t *testing.T) {
	cfg := freshConfig()
	cfg.Features["dev_playground"] = true                                    // would normally add the playground
	cfg.Auth.AllowedClientIDs = []string{"custom-a", "custom-b", "custom-b"} // dedupe; ignore playground
	got := allowedClientIDs(cfg)
	want := []string{"custom-a", "custom-b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("explicit override should win + dedupe: got %v, want %v", got, want)
	}
}

func TestGenerateEnv_WritesKeycloakAllowedClientIDs(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	cfg := freshConfig()
	cfg.Features["dev_playground"] = true

	if err := GenerateAll(root, cfg, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	envBytes, _ := os.ReadFile(filepath.Join(root, ".env"))
	env := string(envBytes)

	// Must contain the primary + playground in that order, comma-joined.
	want := "KEYCLOAK_ALLOWED_CLIENT_IDS=" + cfg.Auth.Client.ID + "," + DevPlaygroundClientID
	if !strings.Contains(env, want) {
		t.Errorf(".env missing %q\nfile:\n%s", want, env)
	}
}

func TestGenerateRealmExport_EmitsIdentityAdminClient_WhenFeatureOn(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	cfg := freshConfig()
	cfg.Features["identity_management"] = true

	if err := GenerateAll(root, cfg, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "deploy/keycloak/realm-export.json"))
	var realm map[string]any
	if err := json.Unmarshal(raw, &realm); err != nil {
		t.Fatalf("parse realm: %v", err)
	}

	// 1. Admin client must be in the clients array.
	clients, _ := realm["clients"].([]any)
	var admin map[string]any
	for _, c := range clients {
		if m, _ := c.(map[string]any); m["clientId"] == IdentityAdminClientID {
			admin = m
		}
	}
	if admin == nil {
		t.Fatalf("identity-admin client %q missing from clients", IdentityAdminClientID)
	}

	// Critical attributes — these gate the entire Sprint 5.1 admin API.
	if admin["publicClient"] != false {
		t.Errorf("admin client must be confidential")
	}
	if admin["serviceAccountsEnabled"] != true {
		t.Errorf("admin client must have serviceAccountsEnabled=true")
	}
	if admin["standardFlowEnabled"] != false {
		t.Errorf("admin client must have standardFlowEnabled=false (no user-facing flows)")
	}
	if admin["directAccessGrantsEnabled"] != false {
		t.Errorf("admin client must have directAccessGrantsEnabled=false (no password grant)")
	}
	if admin["secret"] == "" || admin["secret"] == nil {
		t.Errorf("admin client must carry a secret")
	}

	// 2. The service-account user must exist and be bound to the admin
	//    client with realm-management → realm-admin role.
	users, _ := realm["users"].([]any)
	var svc map[string]any
	for _, u := range users {
		if m, _ := u.(map[string]any); m["serviceAccountClientId"] == IdentityAdminClientID {
			svc = m
		}
	}
	if svc == nil {
		t.Fatalf("service-account user for %q missing — admin API calls would 403 at boot", IdentityAdminClientID)
	}
	cr, _ := svc["clientRoles"].(map[string]any)
	rm, _ := cr["realm-management"].([]any)
	hasAdmin := false
	for _, r := range rm {
		if r == "realm-admin" {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Errorf("service-account must have realm-management→realm-admin; got %v", cr)
	}
}

func TestGenerateRealmExport_OmitsIdentityAdminClient_WhenFeatureOff(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")
	cfg := freshConfig()
	cfg.Features["identity_management"] = false

	if err := GenerateAll(root, cfg, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "deploy/keycloak/realm-export.json"))
	var realm map[string]any
	_ = json.Unmarshal(raw, &realm)

	for _, c := range realm["clients"].([]any) {
		if m, _ := c.(map[string]any); m["clientId"] == IdentityAdminClientID {
			t.Errorf("admin client must not be present when feature is off")
		}
	}
	for _, u := range realm["users"].([]any) {
		if m, _ := u.(map[string]any); m["serviceAccountClientId"] == IdentityAdminClientID {
			t.Errorf("service-account user must not be present when feature is off")
		}
	}
}

func TestGenerateEnv_WritesIdentityAdminCreds(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")

	// feature ON → vars populated with the admin client id + secret
	on := freshConfig()
	on.Features["identity_management"] = true
	if err := GenerateAll(root, on, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll on: %v", err)
	}
	envOn, _ := os.ReadFile(filepath.Join(root, ".env"))
	for _, must := range []string{
		"KEYCLOAK_ADMIN_CLIENT_ID=" + IdentityAdminClientID,
		"KEYCLOAK_ADMIN_CLIENT_SECRET=" + freshSecrets().AdminClientSecret,
	} {
		if !strings.Contains(string(envOn), must) {
			t.Errorf(".env missing %q\nfile:\n%s", must, envOn)
		}
	}

	// feature OFF → vars present but empty (so docker-compose interpolation
	// doesn't fail; the API will treat empty as "admin disabled")
	off := freshConfig()
	off.Features["identity_management"] = false
	if err := GenerateAll(root, off, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll off: %v", err)
	}
	envOff, _ := os.ReadFile(filepath.Join(root, ".env"))
	if !strings.Contains(string(envOff), "KEYCLOAK_ADMIN_CLIENT_ID=\n") {
		t.Errorf("admin client id should be empty when feature off:\n%s", envOff)
	}
}

func TestLoadSecrets_ReadsAdminClientSecret(t *testing.T) {
	root := t.TempDir()
	envContent := "" +
		"KEYCLOAK_ADMIN_CLIENT_SECRET=from-env-admin-client\n" +
		"KEYCLOAK_CLIENT_SECRET=from-env-token-client\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	s := LoadSecrets(root)
	if s.AdminClientSecret != "from-env-admin-client" {
		t.Errorf("admin client secret: got %q", s.AdminClientSecret)
	}
	if s.ClientSecret != "from-env-token-client" {
		t.Errorf("token client secret: got %q", s.ClientSecret)
	}
}

func TestGenerateEnv_EmitsDevPlaygroundVars(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "config", "deploy/keycloak")

	// feature ON → DEV_PLAYGROUND_ENABLED=true
	cfgOn := freshConfig()
	cfgOn.Features["dev_playground"] = true
	if err := GenerateAll(root, cfgOn, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll on: %v", err)
	}
	envOn, _ := os.ReadFile(filepath.Join(root, ".env"))
	if !strings.Contains(string(envOn), "DEV_PLAYGROUND_ENABLED=true") {
		t.Errorf(".env missing DEV_PLAYGROUND_ENABLED=true\n%s", envOn)
	}
	if !strings.Contains(string(envOn), "DEV_PLAYGROUND_CLIENT_ID="+DevPlaygroundClientID) {
		t.Errorf(".env missing DEV_PLAYGROUND_CLIENT_ID=%s\n%s", DevPlaygroundClientID, envOn)
	}

	// feature OFF → DEV_PLAYGROUND_ENABLED=false
	cfgOff := freshConfig()
	cfgOff.Features["dev_playground"] = false
	if err := GenerateAll(root, cfgOff, freshSecrets()); err != nil {
		t.Fatalf("GenerateAll off: %v", err)
	}
	envOff, _ := os.ReadFile(filepath.Join(root, ".env"))
	if !strings.Contains(string(envOff), "DEV_PLAYGROUND_ENABLED=false") {
		t.Errorf(".env missing DEV_PLAYGROUND_ENABLED=false\n%s", envOff)
	}
}

func TestLoadSecrets_ReadsFromEnv(t *testing.T) {
	root := t.TempDir()
	envContent := "" +
		"# comment line\n" +
		"\n" +
		"KEYCLOAK_ADMIN_PASSWORD=from-env-admin\n" +
		"KEYCLOAK_CLIENT_SECRET=from-env-client\n" +
		"SEED_USER_PASSWORD=from-env-seed\n" +
		"UNRELATED=ignored\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	s := LoadSecrets(root)
	if s.AdminPassword != "from-env-admin" {
		t.Errorf("admin: %q", s.AdminPassword)
	}
	if s.ClientSecret != "from-env-client" {
		t.Errorf("client: %q", s.ClientSecret)
	}
	if s.SeedUserPassword != "from-env-seed" {
		t.Errorf("seed: %q", s.SeedUserPassword)
	}
}

func TestLoadSecrets_DefaultsWhenNoEnv(t *testing.T) {
	root := t.TempDir()
	s := LoadSecrets(root)
	if s.AdminPassword == "" || s.ClientSecret == "" || s.SeedUserPassword == "" {
		t.Errorf("expected non-empty defaults, got %+v", s)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"lightweight-saas-backend": "lightweight_saas_backend",
		"My App":                   "my_app",
		"weird!@#$%^name":          "weirdname",
		"":                         "app",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
}
