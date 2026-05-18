package bootstrap

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Prompter wraps stdin/stdout so the prompt code is testable with bytes.Buffer.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

func (p *Prompter) ask(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func (p *Prompter) askBool(label string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	fmt.Fprintf(p.out, "%s [%s]: ", label, d)
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	switch line {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

func (p *Prompter) askInt(label string, def int) int {
	for {
		raw := p.ask(label, strconv.Itoa(def))
		v, err := strconv.Atoi(raw)
		if err == nil {
			return v
		}
		fmt.Fprintf(p.out, "  ! '%s' is not a number\n", raw)
	}
}

// Run drives the interactive prompts, mutating defaults from `current` so
// users get a fast path of "press enter to accept current value".
// Returned Secrets are written to .env only and never persisted in project.json.
func (p *Prompter) Run(current *ProjectConfig, currentSecrets Secrets) (*ProjectConfig, Secrets, error) {
	out := *current

	fmt.Fprintln(p.out, "\n--- project ---")
	out.Project.Name = p.ask("Project name", current.Project.Name)
	out.Project.Environment = p.ask("Environment (local/dev/staging/prod)", current.Project.Environment)

	fmt.Fprintln(p.out, "\n--- auth (Keycloak) ---")
	out.Auth.Realm = p.ask("Realm", current.Auth.Realm)
	out.Auth.Client.ID = p.ask("Client ID", current.Auth.Client.ID)
	out.Auth.Admin.Username = p.ask("Admin username", current.Auth.Admin.Username)
	out.Auth.Admin.Email = p.ask("Admin email", current.Auth.Admin.Email)

	fmt.Fprintln(p.out, "\n--- secrets (stored in .env, not committed) ---")
	secrets := Secrets{
		ClientSecret:     p.ask("Client secret", currentSecrets.ClientSecret),
		AdminPassword:    p.ask("Admin password", currentSecrets.AdminPassword),
		SeedUserPassword: p.ask("Seed user password (shared)", currentSecrets.SeedUserPassword),
	}

	fmt.Fprintln(p.out, "\n--- ports ---")
	out.Ports.API = p.askInt("API port", current.Ports.API)
	out.Ports.Postgres = p.askInt("Postgres port", current.Ports.Postgres)
	out.Ports.Keycloak = p.askInt("Keycloak port", current.Ports.Keycloak)
	out.Ports.KeycloakPostgres = p.askInt("Keycloak Postgres port", current.Ports.KeycloakPostgres)

	fmt.Fprintln(p.out, "\n--- features ---")
	if out.Features == nil {
		out.Features = map[string]bool{}
	}
	for _, f := range []string{"google_login", "mfa", "multi_tenant", "swagger", "seed_users"} {
		out.Features[f] = p.askBool("Enable "+f+"?", current.Features[f])
	}

	if err := out.Validate(); err != nil {
		return nil, Secrets{}, err
	}
	return &out, secrets, nil
}
