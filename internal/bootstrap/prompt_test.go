package bootstrap

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrompter_Run_AcceptsDefaults(t *testing.T) {
	current := freshConfig()
	currentSecrets := freshSecrets()

	// Empty lines = accept every default.
	// We must supply one line per question prompted by Run().
	answers := strings.Repeat("\n", 30)

	in := bytes.NewBufferString(answers)
	var out bytes.Buffer
	p := NewPrompter(in, &out)

	next, secrets, err := p.Run(current, currentSecrets)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if next.Project.Name != current.Project.Name {
		t.Errorf("name changed unexpectedly")
	}
	if next.Ports.API != current.Ports.API {
		t.Errorf("api port changed unexpectedly")
	}
	if secrets.AdminPassword != currentSecrets.AdminPassword {
		t.Errorf("admin password changed")
	}
	if secrets.ClientSecret != currentSecrets.ClientSecret {
		t.Errorf("client secret changed")
	}
}

func TestPrompter_Run_OverridesValues(t *testing.T) {
	current := freshConfig()
	currentSecrets := freshSecrets()

	// Provide explicit values for project name + environment + admin password,
	// accept defaults (empty line) for everything else.
	lines := []string{
		"new-name",  // project name
		"staging",   // environment
		"",          // realm (default)
		"",          // client id (default)
		"",          // admin username (default)
		"",          // admin email (default)
		"",          // client secret (default)
		"new-admin", // admin password
		"",          // seed user password (default)
		"",          // api port
		"",          // postgres port
		"",          // keycloak port
		"",          // keycloak postgres port
		"y",         // google_login
		"",          // mfa
		"",          // multi_tenant
		"",          // swagger
		"",          // seed_users
	}
	input := strings.Join(lines, "\n") + "\n"

	p := NewPrompter(strings.NewReader(input), &bytes.Buffer{})
	next, secrets, err := p.Run(current, currentSecrets)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if next.Project.Name != "new-name" {
		t.Errorf("project name: %q", next.Project.Name)
	}
	if next.Project.Environment != "staging" {
		t.Errorf("environment: %q", next.Project.Environment)
	}
	if secrets.AdminPassword != "new-admin" {
		t.Errorf("admin password: %q", secrets.AdminPassword)
	}
	if !next.Features["google_login"] {
		t.Errorf("google_login should be true")
	}
}

func TestPrompter_Run_RejectsInvalidEnvironment(t *testing.T) {
	current := freshConfig()
	lines := []string{
		current.Project.Name,
		"qa", // invalid — Validate() should reject this
	}
	// Pad enough empty lines so even if Run keeps prompting, it has data.
	input := strings.Join(lines, "\n") + "\n" + strings.Repeat("\n", 30)

	p := NewPrompter(strings.NewReader(input), &bytes.Buffer{})
	_, _, err := p.Run(current, freshSecrets())
	if err == nil {
		t.Fatalf("expected validation error for environment=qa")
	}
}
