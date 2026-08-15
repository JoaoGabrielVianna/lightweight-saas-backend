package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// material builds a deterministic 32-byte key. Fixed rather than random so a
// failure is reproducible; nonces are still random per seal, which is where the
// security property actually lives.
func material(fill byte) []byte { return bytes.Repeat([]byte{fill}, KeySize) }

// testRing builds a one-version keyring, the shape an installation that has
// never rotated has.
func testRing(t *testing.T) *Keyring {
	t.Helper()
	r, err := NewKeyring([]KeyMaterial{{Version: 1, Key: material(0x2a)}}, 1)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return r
}

var testAAD = AAD("connection", "3f2504e0-4f89-41d3-9a0c-0305e82c3301", "client_secret")

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

func TestSealOpen_RoundTrip(t *testing.T) {
	r := testRing(t)

	for _, plaintext := range []string{
		"s3cr3t",
		"a",
		strings.Repeat("long-secret-", 100),
		"unicode: senha-ção-🔐",
		"\x00\x01\x02 binary-ish",
	} {
		sealed, err := r.Seal([]byte(plaintext), testAAD)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plaintext, err)
		}

		opened, err := r.Open(sealed, testAAD)
		if err != nil {
			t.Fatalf("Open(%q): %v", plaintext, err)
		}
		if string(opened) != plaintext {
			t.Errorf("round trip = %q, want %q", opened, plaintext)
		}
	}
}

// TestSeal_CiphertextDoesNotContainPlaintext is the crude but essential check:
// whatever lands in the database must not contain the secret.
func TestSeal_CiphertextDoesNotContainPlaintext(t *testing.T) {
	r := testRing(t)
	const plaintext = "super-secret-client-value"

	sealed, err := r.Seal([]byte(plaintext), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if bytes.Contains(sealed.Ciphertext, []byte(plaintext)) {
		t.Error("ciphertext contains the plaintext")
	}
	if bytes.Contains(sealed.Nonce, []byte(plaintext)) {
		t.Error("nonce contains the plaintext")
	}
	// GCM appends a 16-byte tag, so the ciphertext is longer than the input.
	if len(sealed.Ciphertext) <= len(plaintext) {
		t.Errorf("ciphertext length %d, want > %d (the authentication tag is missing)",
			len(sealed.Ciphertext), len(plaintext))
	}
}

// TestSeal_MetadataIsRecorded pins the fields rotation depends on.
func TestSeal_MetadataIsRecorded(t *testing.T) {
	r, err := NewKeyring([]KeyMaterial{
		{Version: 1, Key: material(0x11)},
		{Version: 7, Key: material(0x77)},
	}, 7)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	sealed, err := r.Seal([]byte("x"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed.Algorithm != AlgorithmAESGCM {
		t.Errorf("algorithm = %q, want %q", sealed.Algorithm, AlgorithmAESGCM)
	}
	// The CURRENT version, not the lowest and not the first supplied.
	if sealed.KeyVersion != 7 {
		t.Errorf("key version = %d, want 7 — a new seal must carry the current version", sealed.KeyVersion)
	}
}

// TestSeal_NonceIsFreshEveryTime is the property GCM's security depends on.
// A repeated nonce under the same key breaks confidentiality outright, so this
// is not a stylistic check.
func TestSeal_NonceIsFreshEveryTime(t *testing.T) {
	r := testRing(t)

	seenNonces := make(map[string]bool, 128)
	seenCiphertexts := make(map[string]bool, 128)

	for i := 0; i < 128; i++ {
		sealed, err := r.Seal([]byte("identical plaintext"), testAAD)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if len(sealed.Nonce) != 12 {
			t.Fatalf("nonce length = %d, want 12 (GCM standard)", len(sealed.Nonce))
		}
		if seenNonces[string(sealed.Nonce)] {
			t.Fatal("nonce reused — GCM's security is void under nonce reuse")
		}
		seenNonces[string(sealed.Nonce)] = true

		// The same plaintext must never produce the same ciphertext, or the
		// database leaks which connections share a secret.
		if seenCiphertexts[string(sealed.Ciphertext)] {
			t.Fatal("identical plaintext produced an identical ciphertext")
		}
		seenCiphertexts[string(sealed.Ciphertext)] = true
	}
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

func TestNewKeyring_RejectsWrongKeySize(t *testing.T) {
	for _, size := range []int{1, 15, 16, 24, 31, 33, 64} {
		_, err := NewKeyring([]KeyMaterial{{Version: 1, Key: bytes.Repeat([]byte{1}, size)}}, 1)
		if !errors.Is(err, ErrKeySize) {
			t.Errorf("NewKeyring with a %d-byte key = %v, want ErrKeySize", size, err)
		}
	}
}

func TestNewKeyring_RejectsEmptyKey(t *testing.T) {
	if _, err := NewKeyring([]KeyMaterial{{Version: 1, Key: nil}}, 1); !errors.Is(err, ErrNoKey) {
		t.Errorf("NewKeyring(nil material) = %v, want ErrNoKey", err)
	}
	if _, err := NewKeyring(nil, 1); !errors.Is(err, ErrEmptyKeyring) {
		t.Errorf("NewKeyring(no keys) = %v, want ErrEmptyKeyring", err)
	}
}

func TestNewKeyringFromBase64_AcceptsEveryCommonSpelling(t *testing.T) {
	raw := material(0x7f)

	for name, encoded := range map[string]string{
		"std":     base64.StdEncoding.EncodeToString(raw),
		"raw std": base64.RawStdEncoding.EncodeToString(raw),
		"url":     base64.URLEncoding.EncodeToString(raw),
		"raw url": base64.RawURLEncoding.EncodeToString(raw),
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := NewKeyringFromBase64([]EncodedKey{{Version: 1, Material: encoded}}, 1)
			if err != nil {
				t.Fatalf("NewKeyringFromBase64: %v", err)
			}
			// Prove it is the same key by round-tripping against a keyring
			// built from the raw bytes.
			fromRaw, err := NewKeyring([]KeyMaterial{{Version: 1, Key: raw}}, 1)
			if err != nil {
				t.Fatalf("NewKeyring: %v", err)
			}
			sealed, err := fromRaw.Seal([]byte("probe"), testAAD)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			opened, err := decoded.Open(sealed, testAAD)
			if err != nil || string(opened) != "probe" {
				t.Errorf("decoded key does not match the raw key: %v", err)
			}
		})
	}
}

func TestDecodeKey_Rejects(t *testing.T) {
	tests := map[string]string{
		"empty":             "",
		"not base64":        "!!!not base64!!!",
		"too short decoded": base64.StdEncoding.EncodeToString([]byte("short")),
		"too long decoded":  base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64)),
		"plausible but 16":  base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)),
	}

	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeKey(encoded)
			if err == nil {
				t.Fatal("expected an error")
			}
			// The key material must never appear in an error that will be logged.
			if encoded != "" && strings.Contains(err.Error(), encoded) {
				t.Errorf("error echoes the key material: %v", err)
			}

			// The same must hold one level up, where the version is added.
			_, ringErr := NewKeyringFromBase64([]EncodedKey{{Version: 3, Material: encoded}}, 3)
			if ringErr == nil {
				t.Fatal("NewKeyringFromBase64 accepted a key DecodeKey rejected")
			}
			if encoded != "" && strings.Contains(ringErr.Error(), encoded) {
				t.Errorf("keyring error echoes the key material: %v", ringErr)
			}
			if !strings.Contains(ringErr.Error(), "3") {
				t.Errorf("keyring error does not name the offending version: %v", ringErr)
			}
		})
	}
}

// TestGenerateKey_RoundTripsThroughEncoding covers the operator bootstrap path.
func TestGenerateKey_RoundTripsThroughEncoding(t *testing.T) {
	raw, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(raw) != KeySize {
		t.Fatalf("generated key is %d bytes, want %d", len(raw), KeySize)
	}

	r, err := NewKeyringFromBase64([]EncodedKey{{Version: 1, Material: EncodeKey(raw)}}, 1)
	if err != nil {
		t.Fatalf("NewKeyringFromBase64(EncodeKey(...)): %v", err)
	}

	sealed, err := r.Seal([]byte("probe"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := r.Open(sealed, testAAD); err != nil {
		t.Errorf("Open: %v", err)
	}

	// Two generated keys must differ.
	other, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if bytes.Equal(raw, other) {
		t.Error("GenerateKey returned the same key twice")
	}
}

// ---------------------------------------------------------------------------
// Open failure modes
// ---------------------------------------------------------------------------

// TestOpen_WrongKeyFails is the point of encrypting at rest: a database dump
// without the master key yields nothing.
//
// Note that both keyrings call the key version 1. This is the WRONG-KEY case,
// not the missing-key case: the version is configured, the material behind it
// is not the one the row was sealed under.
func TestOpen_WrongKeyFails(t *testing.T) {
	sealer := testRing(t)
	sealed, err := sealer.Seal([]byte("s3cr3t"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	other, err := NewKeyring([]KeyMaterial{{Version: 1, Key: material(0x2b)}}, 1) // one byte different
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	got, err := other.Open(sealed, testAAD)
	if !errors.Is(err, ErrOpen) {
		t.Errorf("Open with the wrong key = %v, want ErrOpen", err)
	}
	if errors.Is(err, ErrUnknownKeyVersion) {
		t.Error("wrong key material reported as a missing version — the two are different operator problems")
	}
	if got != nil {
		t.Errorf("Open returned %q alongside an error; it must return nothing", got)
	}
}

// TestOpen_WrongAADFails is the binding property: a ciphertext lifted from one
// connection must not open for another. Without it, an attacker with database
// write access could make connection B authenticate as connection A.
func TestOpen_WrongAADFails(t *testing.T) {
	r := testRing(t)
	sealed, err := r.Seal([]byte("s3cr3t"), AAD("connection", "id-A", "client_secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	wrong := map[string][]byte{
		"different id":    AAD("connection", "id-B", "client_secret"),
		"different field": AAD("connection", "id-A", "other_field"),
		"different kind":  AAD("workspace", "id-A", "client_secret"),
		"empty":           nil,
		"garbage":         []byte("whatever"),
	}

	for name, aad := range wrong {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Open(sealed, aad); !errors.Is(err, ErrOpen) {
				t.Errorf("Open with %s AAD = %v, want ErrOpen", name, err)
			}
		})
	}
}

// TestOpen_TamperedCiphertextFails covers authentication: a modified ciphertext
// must refuse to open rather than decrypt to garbage that would then be sent to
// an identity provider as a client secret.
func TestOpen_TamperedCiphertextFails(t *testing.T) {
	r := testRing(t)
	sealed, err := r.Seal([]byte("s3cr3t-value"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	mutate := map[string]func(Sealed) Sealed{
		"flip a ciphertext bit": func(s Sealed) Sealed {
			c := append([]byte(nil), s.Ciphertext...)
			c[0] ^= 0x01
			s.Ciphertext = c
			return s
		},
		"flip a tag bit": func(s Sealed) Sealed {
			c := append([]byte(nil), s.Ciphertext...)
			c[len(c)-1] ^= 0x01
			s.Ciphertext = c
			return s
		},
		"flip a nonce bit": func(s Sealed) Sealed {
			n := append([]byte(nil), s.Nonce...)
			n[0] ^= 0x01
			s.Nonce = n
			return s
		},
		"truncate the ciphertext": func(s Sealed) Sealed {
			s.Ciphertext = s.Ciphertext[:len(s.Ciphertext)-1]
			return s
		},
		"empty ciphertext": func(s Sealed) Sealed {
			s.Ciphertext = nil
			return s
		},
		"short nonce": func(s Sealed) Sealed {
			s.Nonce = s.Nonce[:6]
			return s
		},
		"empty nonce": func(s Sealed) Sealed {
			s.Nonce = nil
			return s
		},
	}

	for name, mutation := range mutate {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Open(mutation(sealed), testAAD); !errors.Is(err, ErrOpen) {
				t.Errorf("Open after %s = %v, want ErrOpen", name, err)
			}
		})
	}
}

func TestOpen_UnsupportedAlgorithmFails(t *testing.T) {
	r := testRing(t)
	sealed, err := r.Seal([]byte("s3cr3t"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	sealed.Algorithm = "rot13"
	if _, err := r.Open(sealed, testAAD); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Errorf("Open with an unsupported algorithm = %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestSeal_RefusesEmptyPlaintext(t *testing.T) {
	r := testRing(t)

	if _, err := r.Seal(nil, testAAD); err == nil {
		t.Error("Seal(nil) succeeded; an empty secret is a caller bug worth reporting")
	}
	if _, err := r.Seal([]byte{}, testAAD); err == nil {
		t.Error("Seal(empty) succeeded")
	}
}

// TestAAD_Shape pins the binding format. A change here silently makes every
// existing row unopenable, so it is worth a test that fails loudly.
func TestAAD_Shape(t *testing.T) {
	got := string(AAD("connection", "abc", "client_secret"))
	if want := "connection:abc:client_secret"; got != want {
		t.Errorf("AAD = %q, want %q — changing this format orphans every stored secret", got, want)
	}
}
