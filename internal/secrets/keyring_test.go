package secrets

import (
	"errors"
	"strings"
	"testing"
)

// The keyring's contract, tested as security properties rather than as methods.
//
// Every test here answers one of the questions an operator asks during a key
// rotation: can I still read what I wrote, does the system tell me which key is
// missing, and can it ever quietly use a key I did not intend.

// ---------------------------------------------------------------------------
// Version selection
// ---------------------------------------------------------------------------

// TestKeyring_OpensEachRowUnderItsOwnVersion is the central mixed-state
// property: after adding v2 and keeping v1, both generations of row are
// readable.
func TestKeyring_OpensEachRowUnderItsOwnVersion(t *testing.T) {
	v1Only, err := NewKeyring([]KeyMaterial{{Version: 1, Key: material(0xa1)}}, 1)
	if err != nil {
		t.Fatalf("NewKeyring v1: %v", err)
	}
	sealedUnderV1, err := v1Only.Seal([]byte("secret-from-the-v1-era"), testAAD)
	if err != nil {
		t.Fatalf("Seal under v1: %v", err)
	}

	both, err := NewKeyring([]KeyMaterial{
		{Version: 1, Key: material(0xa1)},
		{Version: 2, Key: material(0xb2)},
	}, 2)
	if err != nil {
		t.Fatalf("NewKeyring v1+v2: %v", err)
	}
	sealedUnderV2, err := both.Seal([]byte("secret-from-the-v2-era"), testAAD)
	if err != nil {
		t.Fatalf("Seal under v2: %v", err)
	}
	if sealedUnderV2.KeyVersion != 2 {
		t.Fatalf("new seal used version %d, want the current version 2", sealedUnderV2.KeyVersion)
	}

	opened, err := both.Open(sealedUnderV1, testAAD)
	if err != nil {
		t.Fatalf("open a v1 row with v1 retained: %v", err)
	}
	if string(opened) != "secret-from-the-v1-era" {
		t.Errorf("v1 row opened to %q", opened)
	}

	opened, err = both.Open(sealedUnderV2, testAAD)
	if err != nil {
		t.Fatalf("open a v2 row: %v", err)
	}
	if string(opened) != "secret-from-the-v2-era" {
		t.Errorf("v2 row opened to %q", opened)
	}
}

// TestKeyring_DoesNotTryOtherKeys is the anti-fallback proof, and it is the
// single most important test in this package.
//
// The keyring holds a key that WOULD open the row — under a different version
// number — and a current key that would not. If Open ever searched, this
// passes; it must not. The row names version 1, version 1 is not configured,
// and the answer is a refusal.
func TestKeyring_DoesNotTryOtherKeys(t *testing.T) {
	sealer, err := NewKeyring([]KeyMaterial{{Version: 1, Key: material(0xc3)}}, 1)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	sealed, err := sealer.Seal([]byte("s3cr3t"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// v2 holds the SAME material the row was sealed with, under a different
	// version. A try-every-key implementation opens this row. A version-keyed
	// one refuses it.
	misfiled, err := NewKeyring([]KeyMaterial{{Version: 2, Key: material(0xc3)}}, 2)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	got, err := misfiled.Open(sealed, testAAD)
	if !errors.Is(err, ErrUnknownKeyVersion) {
		t.Errorf("Open = %v, want ErrUnknownKeyVersion — a key that happens to fit must not be tried", err)
	}
	if got != nil {
		t.Errorf("Open returned %q; a refusal must return nothing", got)
	}
}

// TestKeyring_MissingVersionIsDistinctFromWrongKey pins the diagnosis an
// operator acts on. Putting the key back fixes one; nothing fixes the other.
func TestKeyring_MissingVersionIsDistinctFromWrongKey(t *testing.T) {
	sealer, err := NewKeyring([]KeyMaterial{{Version: 1, Key: material(0xd4)}}, 1)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	sealed, err := sealer.Seal([]byte("s3cr3t"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	missing, err := NewKeyring([]KeyMaterial{{Version: 2, Key: material(0xd4)}}, 2)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	wrong, err := NewKeyring([]KeyMaterial{{Version: 1, Key: material(0xee)}}, 1)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	_, missingErr := missing.Open(sealed, testAAD)
	_, wrongErr := wrong.Open(sealed, testAAD)

	if !errors.Is(missingErr, ErrUnknownKeyVersion) || errors.Is(missingErr, ErrOpen) {
		t.Errorf("missing version = %v, want ErrUnknownKeyVersion and not ErrOpen", missingErr)
	}
	if !errors.Is(wrongErr, ErrOpen) || errors.Is(wrongErr, ErrUnknownKeyVersion) {
		t.Errorf("wrong material = %v, want ErrOpen and not ErrUnknownKeyVersion", wrongErr)
	}

	if got := OpenFailureReason(missingErr); got != "unknown_key_version" {
		t.Errorf("OpenFailureReason(missing) = %q", got)
	}
	if got := OpenFailureReason(wrongErr); got != "authentication_failed" {
		t.Errorf("OpenFailureReason(wrong) = %q", got)
	}
}

// TestKeyring_VersionZeroIsRefused. The column defaults to 1 and CHECKs `>= 1`,
// so a zero can only mean the value was never read from a real row. Treating it
// as "whatever this build uses" is try-all-keys wearing a friendly name.
func TestKeyring_VersionZeroIsRefused(t *testing.T) {
	r := testRing(t)
	sealed, err := r.Seal([]byte("s3cr3t"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	sealed.KeyVersion = 0
	if _, err := r.Open(sealed, testAAD); !errors.Is(err, ErrUnknownKeyVersion) {
		t.Errorf("Open with version 0 = %v, want ErrUnknownKeyVersion", err)
	}
}

// TestKeyring_UnknownFutureVersionIsRefused covers the rollback direction: a
// process holding only v1 must refuse a row written by a newer process, not
// mangle it.
func TestKeyring_UnknownFutureVersionIsRefused(t *testing.T) {
	r := testRing(t)
	sealed, err := r.Seal([]byte("s3cr3t"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	sealed.KeyVersion = 99
	_, err = r.Open(sealed, testAAD)
	if !errors.Is(err, ErrUnknownKeyVersion) {
		t.Errorf("Open with a future version = %v, want ErrUnknownKeyVersion", err)
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("error does not name the version the row needs: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewKeyring_RejectsDuplicateVersions(t *testing.T) {
	_, err := NewKeyring([]KeyMaterial{
		{Version: 1, Key: material(0x01)},
		{Version: 1, Key: material(0x02)},
	}, 1)
	if !errors.Is(err, ErrDuplicateKeyVersion) {
		t.Errorf("duplicate version = %v, want ErrDuplicateKeyVersion", err)
	}
}

func TestNewKeyring_RejectsCurrentVersionNotInTheRing(t *testing.T) {
	_, err := NewKeyring([]KeyMaterial{{Version: 1, Key: material(0x01)}}, 2)
	if !errors.Is(err, ErrNoCurrentKey) {
		t.Errorf("current outside the ring = %v, want ErrNoCurrentKey", err)
	}
}

func TestNewKeyring_RejectsVersionBelowOne(t *testing.T) {
	for _, v := range []int{0, -1, -7} {
		_, err := NewKeyring([]KeyMaterial{{Version: v, Key: material(0x01)}}, v)
		if !errors.Is(err, ErrInvalidKeyVersion) {
			t.Errorf("version %d = %v, want ErrInvalidKeyVersion", v, err)
		}
	}
}

// TestKeyring_ConstructionErrorsNeverEchoKeyMaterial. These messages reach the
// boot log of a process that failed to start, which is exactly the log an
// operator pastes into a ticket.
func TestKeyring_ConstructionErrorsNeverEchoKeyMaterial(t *testing.T) {
	const sentinel = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	_, err := NewKeyringFromBase64([]EncodedKey{
		{Version: 1, Material: sentinel},
		{Version: 1, Material: sentinel},
	}, 1)
	if err == nil {
		t.Fatal("expected a duplicate-version error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("error echoes key material: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Inspection
// ---------------------------------------------------------------------------

func TestKeyring_ReportsWhatItHolds(t *testing.T) {
	r, err := NewKeyring([]KeyMaterial{
		{Version: 3, Key: material(0x03)},
		{Version: 1, Key: material(0x01)},
	}, 3)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	if r.CurrentVersion() != 3 {
		t.Errorf("CurrentVersion = %d, want 3", r.CurrentVersion())
	}
	got := r.Versions()
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("Versions = %v, want [1 3] ascending regardless of input order", got)
	}
	if !r.Has(1) || !r.Has(3) || r.Has(2) {
		t.Errorf("Has disagrees with Versions: %v", got)
	}

	// Versions must hand out a copy: "safe to remove" is computed from it.
	got[0] = 999
	if again := r.Versions(); again[0] != 1 {
		t.Error("Versions returned the backing array; a caller mutated the keyring")
	}
}

// TestOpenFailureReason_VocabularyIsClosed.
//
// The returned string becomes a Prometheus label, and internal/metrics keeps an
// ALLOWLIST of exactly these four values rather than importing this package.
// That independence is deliberate — metrics stays free of domain dependencies —
// and this test is what keeps the two halves honest: adding a fifth reason here
// without adding it there makes it count as "other" rather than leak, and this
// test says so out loud at the moment the fifth one appears.
func TestOpenFailureReason_VocabularyIsClosed(t *testing.T) {
	allowed := map[string]bool{
		"unknown_key_version":   true,
		"authentication_failed": true,
		"unsupported_algorithm": true,
		"other":                 true,
	}

	ring := testRing(t)
	sealed, err := ring.Seal([]byte("s3cr3t"), testAAD)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	unknownVersion := sealed
	unknownVersion.KeyVersion = 42
	badAlgorithm := sealed
	badAlgorithm.Algorithm = "rot13"
	tampered := sealed
	tampered.Ciphertext = append([]byte{tampered.Ciphertext[0] ^ 0xFF}, tampered.Ciphertext[1:]...)

	for name, s := range map[string]Sealed{
		"unknown version": unknownVersion,
		"bad algorithm":   badAlgorithm,
		"tampered":        tampered,
	} {
		_, err := ring.Open(s, testAAD)
		if err == nil {
			t.Fatalf("%s: expected a failure", name)
		}
		reason := OpenFailureReason(err)
		if !allowed[reason] {
			t.Errorf("%s produced reason %q, which internal/metrics does not allow — "+
				"it will be counted as \"other\" until the allowlist is updated", name, reason)
		}
	}

	if got := OpenFailureReason(nil); got != "" {
		t.Errorf("OpenFailureReason(nil) = %q, want the empty string", got)
	}
	if got := OpenFailureReason(errors.New("something else entirely")); got != "other" {
		t.Errorf("an unrelated error mapped to %q, want other", got)
	}
}
