// Package secrets seals and opens small values — provider credentials — with
// AES-256-GCM under a versioned keyring held in the process environment.
//
// Threat model, stated plainly so the limits are not mistaken for guarantees.
// This protects a credential at rest in the database: a stolen dump, a leaked
// backup, a replica someone can read. It does NOT protect against an attacker
// who can read the master key, because the running process must be able to open
// what it sealed. Moving the key into a KMS or an HSM is the next step, and the
// stored format is arranged so that step does not require a data migration.
//
// Choices, and why:
//
//   - AES-256-GCM: authenticated encryption. A ciphertext that has been altered
//     fails to open rather than decrypting to garbage that then gets sent to an
//     identity provider as a client secret.
//   - A random 96-bit nonce per seal, never reused. GCM's security collapses on
//     nonce reuse under the same key, so the nonce is generated fresh from
//     crypto/rand for every Seal and stored beside the ciphertext.
//   - Additional authenticated data (AAD) binds a ciphertext to the row it
//     belongs to. Without it, an attacker with write access to the database
//     could move connection A's sealed secret onto connection B and make B
//     authenticate as A. The AAD is not secret; it is context.
//   - A key version travels with every ciphertext, and is the ONLY thing that
//     selects a key when opening it. See keyring.go.
//
// # Two files, two responsibilities
//
//	aesgcm.go   the cipher. One key, one version, seal and open. Knows nothing
//	            about configuration, environments or how many keys exist.
//	keyring.go  key selection. Holds the versions an installation is configured
//	            with, stamps new seals with the current one, and resolves an
//	            existing ciphertext's version to the key that opens it.
//
// The split is what makes rotation a configuration change rather than a
// cryptography change: nothing in this file had to learn about rotation for
// rotation to exist.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// KeySize is the master key length in bytes. 32 bytes selects AES-256.
const KeySize = 32

// AlgorithmAESGCM is the value recorded alongside every ciphertext. Stored
// rather than assumed so a future algorithm change can be told apart from a
// corrupt row.
const AlgorithmAESGCM = "aes-256-gcm"

var (
	// ErrKeySize is returned when a configured key is not KeySize bytes.
	ErrKeySize = errors.New("secrets: master key must be exactly 32 bytes")

	// ErrNoKey is returned when no key material is supplied at all.
	ErrNoKey = errors.New("secrets: no master key configured")

	// ErrOpen is returned when a value cannot be decrypted under the key its
	// stored version names: wrong key material for that version, wrong AAD, or
	// a truncated or tampered ciphertext.
	//
	// Every one of those returns the SAME error on purpose. Distinguishing
	// "wrong key" from "wrong AAD" from "corrupt" tells an attacker which of
	// their guesses was closer, and tells a legitimate operator nothing they
	// can act on that the logs do not already say.
	//
	// It does NOT cover "the keyring has no such version" — that one IS
	// actionable (add the key back, or finish rotating) and has its own
	// sentinel, ErrUnknownKeyVersion.
	ErrOpen = errors.New("secrets: cannot open sealed value")

	// ErrUnsupportedAlgorithm is returned when a stored value names an
	// algorithm this build does not implement.
	ErrUnsupportedAlgorithm = errors.New("secrets: unsupported algorithm")
)

// Sealed is a sealed value together with everything needed to open it later,
// except the key.
//
// The fields map one-to-one onto the connections table's secret_* columns.
// Keeping them separate rather than concatenating into one opaque blob means a
// human reading the schema can see that a nonce exists and that a version is
// recorded — which is the difference between a format that can be rotated and
// one that has to be reverse-engineered first.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyVersion int
	Algorithm  string
}

// key is one version's cipher. Unexported: callers hold a Keyring and never
// choose a key themselves, which is what makes "new secrets always use the
// current version" a property of the type system rather than of a convention.
type key struct {
	version int
	aead    cipher.AEAD
}

// newKey builds the cipher for one version from raw material.
func newKey(version int, material []byte) (*key, error) {
	if version < 1 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidKeyVersion, version)
	}
	if len(material) == 0 {
		return nil, ErrNoKey
	}
	if len(material) != KeySize {
		return nil, fmt.Errorf("%w: got %d", ErrKeySize, len(material))
	}

	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &key{version: version, aead: aead}, nil
}

// seal encrypts plaintext, binding it to aad and stamping this key's version.
func (k *key) seal(plaintext, aad []byte) (Sealed, error) {
	if len(plaintext) == 0 {
		return Sealed{}, errors.New("secrets: refusing to seal an empty value")
	}

	nonce := make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		// A failure here means the system's entropy source is broken. Sealing
		// with a predictable nonce would be far worse than failing.
		return Sealed{}, fmt.Errorf("secrets: read nonce: %w", err)
	}

	return Sealed{
		Ciphertext: k.aead.Seal(nil, nonce, plaintext, aad),
		Nonce:      nonce,
		KeyVersion: k.version,
		Algorithm:  AlgorithmAESGCM,
	}, nil
}

// open decrypts a sealed value, requiring the same aad it was sealed with.
//
// The version re-check is not redundant with the keyring's lookup: it is what
// makes a keyring bug fail loudly instead of quietly. If key selection ever
// hands this function a ciphertext it was not sealed for — the classic mistake
// being "just use the current key" — the answer is a refusal here, before
// AES-GCM is even asked, rather than a plausible-looking decryption failure
// somewhere downstream.
func (k *key) open(s Sealed, aad []byte) ([]byte, error) {
	if s.KeyVersion != k.version {
		return nil, ErrOpen
	}
	if len(s.Nonce) != k.aead.NonceSize() || len(s.Ciphertext) == 0 {
		return nil, ErrOpen
	}

	plaintext, err := k.aead.Open(nil, s.Nonce, s.Ciphertext, aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}

// AAD builds the additional authenticated data binding a sealed value to one
// field of one row.
//
// The shape is `<kind>:<id>:<field>`. Both the id and the field name are
// included: the id stops a ciphertext being moved between rows, and the field
// name stops it being moved between columns of the same row if a second sealed
// field is ever added.
func AAD(kind, id, field string) []byte {
	return []byte(kind + ":" + id + ":" + field)
}

// GenerateKey returns a new random master key, for operators bootstrapping an
// installation or adding a rotation version. Base64-encode it with EncodeKey
// before putting it in the environment.
func GenerateKey() ([]byte, error) {
	material := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, material); err != nil {
		return nil, err
	}
	return material, nil
}

// EncodeKey renders a master key in the form the environment expects.
func EncodeKey(material []byte) string {
	return base64.StdEncoding.EncodeToString(material)
}

// DecodeKey decodes a base64 master key and checks its length.
//
// Both standard and URL-safe alphabets are accepted, with or without padding,
// because an operator pasting a key should not have to know which generator
// produced it.
//
// Exported because configuration validation needs to reject a malformed key
// with a helpful message BEFORE anything tries to build a keyring, and a second
// decoder in the config package would be a second thing to get wrong.
//
// The returned error never quotes the input. That is the whole reason this
// wraps encoding/base64 rather than calling it directly: the standard decoder's
// error message includes the offending bytes, and this input is a master key.
func DecodeKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, ErrNoKey
	}

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		out, err := enc.DecodeString(encoded)
		if err != nil {
			continue
		}
		if len(out) != KeySize {
			return nil, fmt.Errorf("%w: got %d", ErrKeySize, len(out))
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: value is not valid base64", ErrKeySize)
}
