// Package bootstrap manages the project-config source-of-truth.
//
// One JSON file (config/project.json) drives generation of:
//   - .env / .env.example
//   - deploy/keycloak/realm-export.json
//   - (future) docker-compose override files, frontend env, etc.
//
// SECURITY: project.json is committed to source control. It MUST NOT contain
// credentials. All secret fields are tagged `json:"-"` and read separately
// from .env at generation time. See Secrets type and LoadSecrets.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ProjectConfig is the canonical project description. Field shape mirrors
// config/project.json. Fields tagged `json:"-"` exist for in-memory plumbing
// but are intentionally excluded from serialization.
type ProjectConfig struct {
	Project    Project           `json:"project"`
	Auth       Auth              `json:"auth"`
	Ports      Ports             `json:"ports"`
	Features   map[string]bool   `json:"features"`
	SeedUsers  []SeedUser        `json:"seed_users"`
	Meta       map[string]string `json:"_meta,omitempty"`
	JSONSchema string            `json:"$schema,omitempty"`
}

type Project struct {
	Name        string `json:"name"`
	Environment string `json:"environment"` // local | dev | staging | prod
}

type Auth struct {
	Provider string   `json:"provider"` // keycloak | auth0 | supabase | clerk | custom
	Realm    string   `json:"realm"`
	Client   Client   `json:"client"`
	Roles    []string `json:"roles"`
	Admin    Admin    `json:"admin"`
	// AllowedClientIDs optionally overrides the auto-derived list of token-
	// issuing client ids. When empty, the generator derives:
	//   - auth.client.id  (always)
	//   - DevPlaygroundClientID  (when features.dev_playground=true)
	AllowedClientIDs []string `json:"allowed_client_ids,omitempty"`
}

type Client struct {
	ID string `json:"id"`
	// Secret never serializes: it lives in .env as KEYCLOAK_CLIENT_SECRET.
	Secret string `json:"-"`
}

type Admin struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	// Password never serializes: it lives in .env as KEYCLOAK_ADMIN_PASSWORD.
	Password string `json:"-"`
}

type Ports struct {
	API              int `json:"api"`
	Postgres         int `json:"postgres"`
	Keycloak         int `json:"keycloak"`
	KeycloakPostgres int `json:"keycloak_postgres"`
}

type SeedUser struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	// Password never serializes: all seed users share .env's SEED_USER_PASSWORD.
	Password string `json:"-"`
}

// Load reads and validates a project config file. Schema validation is run
// first (structural correctness), then Validate() runs (semantic checks).
func Load(path string) (*ProjectConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := validateAgainstSchema(b); err != nil {
		return nil, fmt.Errorf("schema %s: %w", path, err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes the project config back to disk with stable formatting.
// Secret fields (tagged json:"-") are NOT written.
func Save(path string, cfg *ProjectConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Validate enforces the minimum field set required for generation.
// Per-generator validation lives in each generator.
func (c *ProjectConfig) Validate() error {
	missing := []string{}
	if c.Project.Name == "" {
		missing = append(missing, "project.name")
	}
	if c.Project.Environment == "" {
		missing = append(missing, "project.environment")
	}
	if c.Auth.Provider == "" {
		missing = append(missing, "auth.provider")
	}
	if c.Auth.Realm == "" {
		missing = append(missing, "auth.realm")
	}
	if c.Auth.Client.ID == "" {
		missing = append(missing, "auth.client.id")
	}
	if c.Ports.API == 0 {
		missing = append(missing, "ports.api")
	}
	if c.Ports.Keycloak == 0 {
		missing = append(missing, "ports.keycloak")
	}
	if c.Ports.Postgres == 0 {
		missing = append(missing, "ports.postgres")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing fields: %v", missing)
	}
	if c.Auth.Provider != "keycloak" {
		return errors.New("only auth.provider=\"keycloak\" is implemented today")
	}
	validEnvs := map[string]bool{"local": true, "dev": true, "staging": true, "prod": true}
	if !validEnvs[c.Project.Environment] {
		return fmt.Errorf("project.environment must be one of local|dev|staging|prod, got %q", c.Project.Environment)
	}
	return nil
}
