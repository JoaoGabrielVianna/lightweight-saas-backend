package config

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
)

// The secret-keyring configuration contract.
//
// # The variables
//
//	SECRETS_KEYRING      1:<base64>,2:<base64>   every key the process can decrypt with
//	SECRETS_KEY_CURRENT  2                       the one version new secrets are sealed under
//	SECRETS_MASTER_KEY   <base64>                LEGACY. Equivalent to SECRETS_KEYRING=1:<base64>
//
// # Why one variable holding several keys, and not SECRETS_MASTER_KEY_V1, _V2, …
//
// A per-version variable name reads better and cannot be built. This project's
// configuration is guarded by a drift gate (contract_test.go) that matches the
// literal `getEnv("NAME"` calls in config.go against a declared table, against
// `.env.example` and against the compose file. A variable whose NAME is
// computed at runtime is invisible to all four, which would mean the one part
// of the configuration that can render an installation unreadable is also the
// one part nothing checks. A fixed pair of names keeps the gate awake.
//
// It also keeps the operator story small: two variables to set, in a .env, in a
// compose file, or in a VPS secret manager — no Kubernetes, no Vault.
//
// # Why the version is explicit and not positional
//
// Because the version is written into every row, forever. A positional list
// ("the first is v1") would silently renumber every key the day someone removes
// a retired one from the middle, and the rows pointing at the old numbering
// would open with the wrong key or not at all. Writing `2:` costs two
// characters and makes the mapping impossible to lose.
//
// # Why the legacy variable is normalised here and nowhere else
//
// SECRETS_MASTER_KEY is what every existing installation has, and existing rows
// were sealed under `secret_key_version = 1` — the schema's DEFAULT since
// 000003 and the only value the pre-rotation code ever stamped. So the legacy
// variable maps to exactly `1:<key>` with current = 1, and that mapping happens
// at parse time. Past this file nothing knows the legacy variable exists: the
// server, the resolver, the rotator and the CLI all see a normalised keyring.
// Two authorities that both answer "which key" is how an installation ends up
// sealing under one key and trying to open under another.
//
// Setting BOTH is refused rather than merged. There is no ordering of the two
// that is obviously right, and guessing produces the failure this whole slice
// exists to prevent.

// EnvKeyring is the variable holding every configured key version.
const EnvKeyring = "SECRETS_KEYRING"

// EnvKeyCurrent is the variable nominating the encryption version.
const EnvKeyCurrent = "SECRETS_KEY_CURRENT"

// EnvMasterKey is the pre-rotation variable, still honoured.
const EnvMasterKey = "SECRETS_MASTER_KEY"

// LegacyKeyVersion is the version legacy installations' rows carry.
//
// Not a guess: migrations/000003_connections.up.sql declares
// `secret_key_version int NOT NULL DEFAULT 1`, and the pre-rotation sealer
// stamped a hard-coded 1 on everything it wrote. Every row in every existing
// installation is therefore version 1.
const LegacyKeyVersion = 1

// KeyringConfig is the normalised model. It is what the runtime consumes; the
// environment's spelling stops at this package.
type KeyringConfig struct {
	// Keys is every configured version, ascending by version.
	Keys []secrets.EncodedKey

	// Current is the version new secrets are sealed under.
	Current int
}

// Configured reports whether any key is configured at all.
//
// False is a legitimate deployment state, not an error: without a key the
// connection API and the workspace-scoped identity runtime are not mounted, and
// an installation that never stores a provider credential needs no key.
func (k KeyringConfig) Configured() bool { return len(k.Keys) > 0 }

// Keyring parses and normalises the secret key configuration.
//
// Returns a zero KeyringConfig and no error when nothing is configured. Every
// error is safe to log: they name variables and version numbers, never
// material.
func (c *Config) Keyring() (KeyringConfig, error) {
	return parseKeyring(c.SecretsKeyringSpec, c.SecretsKeyCurrent, c.SecretsMasterKey)
}

// parseKeyring is the whole contract in one function, so there is exactly one
// place where "which key is current" is decided.
func parseKeyring(spec, current, legacy string) (KeyringConfig, error) {
	spec = strings.TrimSpace(spec)
	current = strings.TrimSpace(current)
	legacy = strings.TrimSpace(legacy)

	if spec != "" && legacy != "" {
		return KeyringConfig{}, fmt.Errorf(
			"%s and %s are both set — they are two answers to the same question and "+
				"there is no safe order to apply them in. Move the legacy key into the "+
				"keyring as version %d and unset %s",
			EnvKeyring, EnvMasterKey, LegacyKeyVersion, EnvMasterKey)
	}

	// ── Legacy bridge ───────────────────────────────────────────────────────
	if spec == "" {
		if legacy == "" {
			if current != "" {
				return KeyringConfig{}, fmt.Errorf(
					"%s is set but no keys are configured — set %s (or %s)",
					EnvKeyCurrent, EnvKeyring, EnvMasterKey)
			}
			return KeyringConfig{}, nil
		}
		if current != "" && current != strconv.Itoa(LegacyKeyVersion) {
			return KeyringConfig{}, fmt.Errorf(
				"%s=%s but the only configured key is the legacy %s, which is version %d. "+
					"To seal under a different version, move to %s",
				EnvKeyCurrent, current, EnvMasterKey, LegacyKeyVersion, EnvKeyring)
		}
		if _, err := secrets.DecodeKey(legacy); err != nil {
			return KeyringConfig{}, fmt.Errorf("%s: %w", EnvMasterKey, redactKeyError(err))
		}
		return KeyringConfig{
			Keys:    []secrets.EncodedKey{{Version: LegacyKeyVersion, Material: legacy}},
			Current: LegacyKeyVersion,
		}, nil
	}

	// ── Keyring ─────────────────────────────────────────────────────────────
	keys, err := parseKeyringSpec(spec)
	if err != nil {
		return KeyringConfig{}, err
	}

	chosen, err := chooseCurrent(keys, current)
	if err != nil {
		return KeyringConfig{}, err
	}
	return KeyringConfig{Keys: keys, Current: chosen}, nil
}

// parseKeyringSpec turns `1:AAA,2:BBB` into versioned entries.
func parseKeyringSpec(spec string) ([]secrets.EncodedKey, error) {
	seen := map[int]bool{}
	var keys []secrets.EncodedKey

	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			// A trailing comma is a typo, not a key. Skipping it silently is
			// fine; there is nothing an operator would want it to mean.
			continue
		}

		version, material, found := strings.Cut(entry, ":")
		if !found {
			return nil, fmt.Errorf(
				"%s has an entry with no version prefix — every entry must be "+
					"`<version>:<base64 key>`, e.g. `1:$(openssl rand -base64 32)` "+
					"(value withheld)", EnvKeyring)
		}

		n, err := strconv.Atoi(strings.TrimSpace(version))
		if err != nil {
			return nil, fmt.Errorf(
				"%s has an entry whose version %q is not a whole number", EnvKeyring, strings.TrimSpace(version))
		}
		if n < 1 {
			return nil, fmt.Errorf(
				"%s has version %d; versions start at 1 (the schema CHECKs it)", EnvKeyring, n)
		}
		if seen[n] {
			return nil, fmt.Errorf(
				"%s configures version %d more than once — which key an installation "+
					"encrypts with must not depend on the order of a variable", EnvKeyring, n)
		}

		material = strings.TrimSpace(material)
		if _, err := secrets.DecodeKey(material); err != nil {
			return nil, fmt.Errorf("%s version %d: %w", EnvKeyring, n, redactKeyError(err))
		}

		seen[n] = true
		keys = append(keys, secrets.EncodedKey{Version: n, Material: material})
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("%s is set but contains no keys", EnvKeyring)
	}

	sort.Slice(keys, func(i, j int) bool { return keys[i].Version < keys[j].Version })
	return keys, nil
}

// chooseCurrent resolves which version seals new secrets.
//
// One key and no nomination is unambiguous, so it is allowed — that is the
// shape of an installation that has not rotated yet, and demanding a second
// variable to state the obvious would be friction with no safety in it. Two or
// more keys with no nomination is refused: "the highest version" is a plausible
// rule that is wrong exactly once, during a rollback, and being wrong there
// means sealing new secrets under a key the operator is retiring.
func chooseCurrent(keys []secrets.EncodedKey, current string) (int, error) {
	if current == "" {
		if len(keys) == 1 {
			return keys[0].Version, nil
		}
		return 0, fmt.Errorf(
			"%s configures %d versions but %s is unset — with more than one key, which "+
				"one new secrets are sealed under has to be stated, not inferred",
			EnvKeyring, len(keys), EnvKeyCurrent)
	}

	n, err := strconv.Atoi(current)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number (got %q)", EnvKeyCurrent, current)
	}
	for _, k := range keys {
		if k.Version == n {
			return n, nil
		}
	}
	return 0, fmt.Errorf(
		"%s=%d, but %s configures %s — new secrets would be sealed with a key this "+
			"process does not hold", EnvKeyCurrent, n, EnvKeyring, versionList(keys))
}

func versionList(keys []secrets.EncodedKey) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, strconv.Itoa(k.Version))
	}
	return strings.Join(parts, ", ")
}

// redactKeyError turns a decode failure into the sentence an operator acts on.
//
// The two failures stay distinguishable because the fixes differ: "not base64"
// means the value was mangled in transit (a shell ate it, or newlines crept
// in), while a byte count means the wrong generator was used. Both state the
// requirement, so the fix is one command.
//
// secrets.DecodeKey never quotes its input, so what it reports — a length, or
// the word "base64" — is already safe to log. This wrapper adds the remedy and
// is the boundary at which that safety is asserted rather than assumed:
// TestValidate_MasterKey checks every message against the key it rejected.
func redactKeyError(err error) error {
	const remedy = " — generate one with: openssl rand -base64 32"

	switch {
	case errors.Is(err, secrets.ErrNoKey):
		return errors.New("the key is empty" + remedy)
	case errors.Is(err, secrets.ErrKeySize):
		// DecodeKey's message is either "got N" or "value is not valid base64".
		// Carrying it through is what keeps the byte count in the message.
		return fmt.Errorf("%s (value withheld)%s", keySizeDetail(err), remedy)
	default:
		return errors.New("the key could not be read (value withheld)" + remedy)
	}
}

// keySizeDetail renders the length problem without echoing the value.
func keySizeDetail(err error) string {
	msg := err.Error()
	if idx := strings.LastIndex(msg, "got "); idx >= 0 {
		return "the key decodes to " + msg[idx+len("got "):] + " bytes, need exactly " +
			strconv.Itoa(secrets.KeySize)
	}
	return "the key is not valid base64"
}

// LoadSecretsKeyring reads and normalises the keyring from the environment
// alone, without the rest of the configuration.
//
// This exists for the `secrets` CLI, which needs a keyring and a DSN and
// nothing else. Going through LoadConfig would make a key-rotation command
// refuse to run because some unrelated Keycloak variable is missing — the same
// reason cmd/migrate does not use it either.
func LoadSecretsKeyring() (KeyringConfig, error) {
	return parseKeyring(
		getEnv(EnvKeyring, ""),
		getEnv(EnvKeyCurrent, ""),
		getEnv(EnvMasterKey, ""),
	)
}
