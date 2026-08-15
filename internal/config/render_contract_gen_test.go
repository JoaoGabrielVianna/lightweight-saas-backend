package config

import (
	"os"
	"testing"
)

// TestRenderContract_ToFile writes the published configuration matrix.
//
// A test rather than a `go:generate` program, because it needs nothing a test
// does not already have and because it means `go test ./internal/config` keeps
// proving the renderer works even when nobody is regenerating docs.
//
// Only runs when asked:
//
//	LW_WRITE_CONTRACT=path/to/file go test ./internal/config -run TestRenderContract_ToFile
func TestRenderContract_ToFile(t *testing.T) {
	path := os.Getenv("LW_WRITE_CONTRACT")
	if path == "" {
		t.Skip("LW_WRITE_CONTRACT unset — nothing to write")
	}
	if err := os.WriteFile(path, []byte(RenderContractTable()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
