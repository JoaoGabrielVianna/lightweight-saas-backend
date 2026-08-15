package server

import (
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
)

// The census reporting, without a database. What it queries is covered against
// real PostgreSQL in internal/connection; what is worth pinning here is the
// text an operator reads at boot and the fact that a keyless deployment stays
// silent instead of crashing.

// TestStartSecretKeyCensus_NoOpWithoutAKeyring. An installation with no keyring
// has no sealed credentials, so there is nothing to count — and starting a
// goroutine that dereferences a nil rotator every five minutes would turn a
// legitimate deployment into a crash loop.
func TestStartSecretKeyCensus_NoOpWithoutAKeyring(t *testing.T) {
	// Would panic on a nil dereference if the guard were removed.
	StartSecretKeyCensus(nil)
}

// TestSetupSecretKeyCensus_FollowsTheConfiguration.
func TestSetupSecretKeyCensus_FollowsTheConfiguration(t *testing.T) {
	t.Run("no keyring means no census", func(t *testing.T) {
		if got := SetupSecretKeyCensus(nil, &config.Config{}); got != nil {
			t.Error("a census was built for an installation with no keyring")
		}
	})

	t.Run("a malformed keyring means no census, not a panic", func(t *testing.T) {
		// SetupConnection has already refused the boot on this; reaching here
		// with bad configuration should be quiet rather than fatal a second time.
		cfg := &config.Config{SecretsKeyringSpec: "1:!!!not-base64!!!"}
		if got := SetupSecretKeyCensus(nil, cfg); got != nil {
			t.Error("a census was built from an unparseable keyring")
		}
	})
}

// TestDescribeKeyring_ShowsVersionsAndNothingElse.
//
// This string goes into the boot log. An operator reads it to confirm the
// process came up holding the keys they think it holds, which is a list of
// small integers — and is the only part of a keyring that is safe to print.
func TestDescribeKeyring_ShowsVersionsAndNothingElse(t *testing.T) {
	const material = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=" // 32 × 'A'
	const other = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="    // 32 × 'B'

	ring, err := secrets.NewKeyringFromBase64([]secrets.EncodedKey{
		{Version: 1, Material: material},
		{Version: 2, Material: other},
	}, 2)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}

	got := describeKeyring(ring)
	if !strings.Contains(got, "v1") || !strings.Contains(got, "v2 (current)") {
		t.Errorf("describeKeyring = %q, want both versions with the current one marked", got)
	}
	for _, secret := range []string{material, other, material[:20], other[:20]} {
		if strings.Contains(got, secret) {
			t.Errorf("describeKeyring leaked key material: %q", got)
		}
	}
}

// TestDescribeCensus_RendersTheDistribution.
func TestDescribeCensus_RendersTheDistribution(t *testing.T) {
	ring, err := secrets.NewKeyringFromBase64([]secrets.EncodedKey{
		{Version: 1, Material: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="},
		{Version: 2, Material: "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="},
	}, 2)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}

	census := connection.KeyVersionCensus{Rows: map[int]int64{1: 3, 2: 11}}
	got := describeCensus(census, ring)
	for _, want := range []string{"v1=3", "v2=11", "(current)"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeCensus = %q, want it to contain %q", got, want)
		}
	}

	empty := describeCensus(connection.KeyVersionCensus{Rows: map[int]int64{}}, ring)
	if !strings.Contains(empty, "no credentials stored") {
		t.Errorf("an empty installation renders as %q", empty)
	}
}

func TestJoinVersions(t *testing.T) {
	if got := joinVersions([]int{1, 3}); got != "v1, v3" {
		t.Errorf("joinVersions = %q, want \"v1, v3\"", got)
	}
	if got := joinVersions(nil); got != "" {
		t.Errorf("joinVersions(nil) = %q, want empty", got)
	}
}
