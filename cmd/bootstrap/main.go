// Command bootstrap is the interactive project initializer.
//
//	go run ./cmd/bootstrap                   # prompts, then regenerates
//	go run ./cmd/bootstrap -non-interactive  # regenerate from current config+.env only
//	go run ./cmd/bootstrap -config X.json    # use an alternative source file
//
// Generated files (overwritten in place):
//   - .env
//   - .env.example
//   - config/project.schema.json (mirror of the embedded canonical schema)
//   - deploy/keycloak/realm-export.json
//
// Source-of-truth (committed): config/project.json
// Source-of-secrets (gitignored): .env
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/bootstrap"
)

func main() {
	var (
		configPath     = flag.String("config", "config/project.json", "path to project config JSON")
		nonInteractive = flag.Bool("non-interactive", false, "skip prompts; regenerate from current config + .env")
	)
	flag.Parse()

	repoRoot, err := findRepoRoot(*configPath)
	if err != nil {
		fatal(err)
	}
	absConfig := filepath.Join(repoRoot, *configPath)

	current, err := bootstrap.Load(absConfig)
	if err != nil {
		fatal(err)
	}
	currentSecrets := bootstrap.LoadSecrets(repoRoot)

	var (
		next    = current
		secrets = currentSecrets
	)
	if !*nonInteractive {
		fmt.Println("Bootstrap — interactive mode. Press Enter to accept the [default] value.")
		p := bootstrap.NewPrompter(os.Stdin, os.Stdout)
		next, secrets, err = p.Run(current, currentSecrets)
		if err != nil {
			fatal(err)
		}
		if err := bootstrap.Save(absConfig, next); err != nil {
			fatal(err)
		}
		fmt.Println("+ wrote", absConfig)
	}

	if err := bootstrap.GenerateAll(repoRoot, next, secrets); err != nil {
		fatal(err)
	}
	fmt.Println("+ regenerated .env, .env.example, config/project.schema.json, deploy/keycloak/realm-export.json")
	fmt.Println("Next: make up   # to start the stack")
}

// findRepoRoot walks up from cwd looking for the config file so the CLI
// works from any subdirectory.
func findRepoRoot(rel string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate %s from %s upwards", rel, cwd)
		}
		dir = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "bootstrap error:", err)
	os.Exit(1)
}
