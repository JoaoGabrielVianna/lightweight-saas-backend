package config

import (
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
)

// The keyring configuration contract, tested as the operator-facing behaviour
// it is. Every case here is a shape someone will type into a .env at some
// point, and the requirement is that the wrong ones are refused with a sentence
// that says what to do, and that none of them ever echo a key.

// Three distinct, obviously fake 32-byte keys. The values matter only in that
// they differ, so a test can prove the right one was selected.
const (
	keyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=" // 32 × 'A'
	keyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=" // 32 × 'B'
	keyC = "Q0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0M=" // 32 × 'C'
)

func TestKeyring_Unconfigured(t *testing.T) {
	got, err := parseKeyring("", "", "")
	if err != nil {
		t.Fatalf("an installation with no keys is legitimate, got: %v", err)
	}
	if got.Configured() {
		t.Error("Configured() is true with nothing set")
	}
}

// TestKeyring_LegacyMasterKeyBecomesVersionOne is the backward-compatibility
// promise. Every existing installation has SECRETS_MASTER_KEY, and every
// existing row is `secret_key_version = 1` — the schema's DEFAULT since 000003.
// The mapping must be exactly that, or an upgrade orphans every credential.
func TestKeyring_LegacyMasterKeyBecomesVersionOne(t *testing.T) {
	got, err := parseKeyring("", "", keyA)
	if err != nil {
		t.Fatalf("parseKeyring: %v", err)
	}
	if !got.Configured() {
		t.Fatal("the legacy key produced no keyring")
	}
	if len(got.Keys) != 1 || got.Keys[0].Version != LegacyKeyVersion {
		t.Fatalf("keys = %+v, want exactly version %d", got.Keys, LegacyKeyVersion)
	}
	if got.Keys[0].Material != keyA {
		t.Error("the legacy key material was not carried through")
	}
	if got.Current != LegacyKeyVersion {
		t.Errorf("current = %d, want %d", got.Current, LegacyKeyVersion)
	}

	// And it builds a working keyring, which is the claim that actually matters.
	ring, err := secrets.NewKeyringFromBase64(got.Keys, got.Current)
	if err != nil {
		t.Fatalf("build keyring from the legacy config: %v", err)
	}
	if ring.CurrentVersion() != 1 || !ring.Has(1) {
		t.Errorf("keyring holds %v, current v%d", ring.Versions(), ring.CurrentVersion())
	}
}

// TestKeyring_LegacyAndModernTogetherIsRefused. Two authorities answering
// "which key" is the failure this whole slice exists to prevent, and there is
// no ordering of the two that is obviously right.
func TestKeyring_LegacyAndModernTogetherIsRefused(t *testing.T) {
	_, err := parseKeyring("1:"+keyA, "1", keyB)
	if err == nil {
		t.Fatal("both variables set was accepted")
	}
	for _, want := range []string{EnvKeyring, EnvMasterKey} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
}

func TestKeyring_ParsesVersionedEntries(t *testing.T) {
	got, err := parseKeyring("2:"+keyB+",1:"+keyA, "2", "")
	if err != nil {
		t.Fatalf("parseKeyring: %v", err)
	}
	if len(got.Keys) != 2 {
		t.Fatalf("keys = %+v, want two", got.Keys)
	}
	// Sorted by version regardless of the order they were written in, because
	// "safe to remove" and every log line read better ascending.
	if got.Keys[0].Version != 1 || got.Keys[1].Version != 2 {
		t.Errorf("keys are not sorted ascending: %+v", got.Keys)
	}
	if got.Keys[0].Material != keyA || got.Keys[1].Material != keyB {
		t.Error("a version was paired with the wrong material")
	}
	if got.Current != 2 {
		t.Errorf("current = %d, want 2", got.Current)
	}
}

func TestKeyring_ToleratesWhitespaceAndTrailingCommas(t *testing.T) {
	got, err := parseKeyring("  1: "+keyA+" , 2:"+keyB+" , ", " 2 ", "")
	if err != nil {
		t.Fatalf("parseKeyring: %v", err)
	}
	if len(got.Keys) != 2 || got.Current != 2 {
		t.Errorf("got %+v current=%d", got.Keys, got.Current)
	}
}

// TestKeyring_SingleKeyNeedsNoCurrent — an installation that has not rotated
// should not have to state the obvious.
func TestKeyring_SingleKeyNeedsNoCurrent(t *testing.T) {
	got, err := parseKeyring("3:"+keyC, "", "")
	if err != nil {
		t.Fatalf("parseKeyring: %v", err)
	}
	if got.Current != 3 {
		t.Errorf("current = %d, want the only configured version", got.Current)
	}
}

// TestKeyring_MultipleKeysRequireAnExplicitCurrent.
//
// "The highest version" is a plausible rule that is wrong exactly once — during
// a rollback — and being wrong there means sealing new secrets with the key
// being retired. So it is refused rather than inferred.
func TestKeyring_MultipleKeysRequireAnExplicitCurrent(t *testing.T) {
	_, err := parseKeyring("1:"+keyA+",2:"+keyB, "", "")
	if err == nil {
		t.Fatal("two keys with no SECRETS_KEY_CURRENT was accepted")
	}
	if !strings.Contains(err.Error(), EnvKeyCurrent) {
		t.Errorf("the error does not name %s: %v", EnvKeyCurrent, err)
	}
}

func TestKeyring_Rejects(t *testing.T) {
	tests := map[string]struct {
		spec, current, legacy string
		mustMention           string
	}{
		"duplicate version": {
			spec: "1:" + keyA + ",1:" + keyB, current: "1",
			mustMention: "more than once",
		},
		"version zero": {
			spec: "0:" + keyA, current: "0",
			mustMention: "versions start at 1",
		},
		"negative version": {
			spec: "-1:" + keyA, current: "-1",
			mustMention: "versions start at 1",
		},
		"non-numeric version": {
			spec: "one:" + keyA, current: "1",
			mustMention: "not a whole number",
		},
		"no version prefix": {
			spec: keyA, current: "1",
			mustMention: "no version prefix",
		},
		"current not in the ring": {
			spec: "1:" + keyA + ",2:" + keyB, current: "3",
			mustMention: "does not hold",
		},
		"current is not a number": {
			spec: "1:" + keyA, current: "latest",
			mustMention: "whole number",
		},
		"malformed key material": {
			spec: "1:!!!not-base64!!!", current: "1",
			mustMention: "not valid base64",
		},
		"short key material": {
			spec: "1:c2hvcnQ=", current: "1",
			mustMention: "5 bytes",
		},
		"empty keyring": {
			spec: " , ", current: "1",
			mustMention: "contains no keys",
		},
		"current without any key": {
			current:     "2",
			mustMention: "no keys are configured",
		},
		"legacy key with a foreign current": {
			legacy: keyA, current: "2",
			mustMention: EnvKeyring,
		},
		"malformed legacy key": {
			legacy:      "!!!nope!!!",
			mustMention: "not valid base64",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseKeyring(tc.spec, tc.current, tc.legacy)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.mustMention) {
				t.Errorf("error %q does not mention %q — an operator has to know what to fix",
					err, tc.mustMention)
			}
		})
	}
}

// TestKeyring_ErrorsNeverEchoKeyMaterial.
//
// These messages go to the boot log of a process that refused to start, which
// is exactly the log an operator pastes into a ticket or a chat. A key that
// reaches one of them has left the machine.
func TestKeyring_ErrorsNeverEchoKeyMaterial(t *testing.T) {
	cases := []struct{ spec, current, legacy string }{
		{spec: "1:" + keyA + ",1:" + keyB, current: "1"},
		{spec: "1:" + keyA + ",2:" + keyB, current: "9"},
		{spec: "1:" + keyA, current: "later"},
		{spec: "0:" + keyA, current: "0"},
		{spec: keyA, current: "1"},
		{spec: "1:" + keyA, current: "1", legacy: keyB},
		{legacy: keyC, current: "7"},
	}

	for _, tc := range cases {
		_, err := parseKeyring(tc.spec, tc.current, tc.legacy)
		if err == nil {
			t.Fatalf("expected a rejection for %+v", tc)
		}
		for label, material := range map[string]string{"keyA": keyA, "keyB": keyB, "keyC": keyC} {
			if strings.Contains(err.Error(), material) {
				t.Errorf("error for %+v echoes %s: %v", tc, label, err)
			}
		}
	}
}

// TestKeyring_ValidateAgreesWithTheParser closes the loop the whole design
// depends on: a configuration that boots is one the server and the rotation CLI
// can both build a keyring from, because all three go through one function.
func TestKeyring_ValidateAgreesWithTheParser(t *testing.T) {
	t.Run("a bad keyring fails validation", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.SecretsKeyringSpec = "1:" + keyA + ",2:" + keyB
		cfg.SecretsKeyCurrent = "5"

		problems := cfg.validationProblems()
		if len(problems) == 0 {
			t.Fatal("a current version outside the keyring booted")
		}
		for _, p := range problems {
			if strings.Contains(p, keyA) || strings.Contains(p, keyB) {
				t.Errorf("a validation problem echoed key material: %q", p)
			}
		}
	})

	t.Run("a good keyring passes and builds", func(t *testing.T) {
		cfg := validConfigFixture()
		cfg.SecretsKeyringSpec = "1:" + keyA + ",2:" + keyB
		cfg.SecretsKeyCurrent = "2"

		if problems := cfg.validationProblems(); mentions(problems, EnvKeyring) {
			t.Fatalf("a valid keyring was rejected: %v", problems)
		}
		kc, err := cfg.Keyring()
		if err != nil {
			t.Fatalf("Keyring: %v", err)
		}
		ring, err := secrets.NewKeyringFromBase64(kc.Keys, kc.Current)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if ring.CurrentVersion() != 2 || !ring.Has(1) {
			t.Errorf("built keyring holds %v, current v%d", ring.Versions(), ring.CurrentVersion())
		}
	})
}
