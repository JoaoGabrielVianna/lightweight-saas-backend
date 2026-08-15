package secrets

import (
	"errors"
	"fmt"
	"sort"
)

// A bounded keyring: one key an installation encrypts with, plus the older keys
// it must still be able to decrypt with.
//
// # The rule this type exists to enforce
//
// A ciphertext is opened with the key its OWN stored version names, and with no
// other. There is no "try every key until one works" path here, and adding one
// would be a security regression rather than a convenience:
//
//   - it turns a missing key from a diagnosable error into a slow, silent
//     failure that looks identical to corruption;
//   - it makes an attacker's forged ciphertext cost N authentication attempts
//     instead of one, which is exactly the oracle AES-GCM's tag exists to deny;
//   - and it removes the only signal that says rotation has not finished, which
//     is the signal an operator needs before destroying an old key.
//
// The database already stores `secret_key_version` on every row. Resolution is:
//
//	row.secret_key_version → Keyring.Open → that version's key → AES-GCM open
//
// An unknown version is ErrUnknownKeyVersion, deliberately distinct from
// ErrOpen. The two mean different things to whoever is holding the pager:
// ErrUnknownKeyVersion means "a key this installation used is not configured
// any more" and is fixed by putting it back; ErrOpen means "the key IS
// configured and does not open this row", which is wrong key material or a
// tampered row and is not fixed by anything the operator can type.
//
// # What is deliberately absent
//
// No key derivation, no envelope encryption, no external KMS. The keyring holds
// what the process was configured with. The seam is here — an implementation
// that fetched a key from a KMS would satisfy the same lookup — but nothing in
// this slice reaches outside the process for a key.

var (
	// ErrUnknownKeyVersion is returned when a sealed value names a key version
	// this process was not configured with. Actionable, and therefore NOT
	// folded into ErrOpen.
	ErrUnknownKeyVersion = errors.New("secrets: no key configured for this version")

	// ErrInvalidKeyVersion is returned for a version below 1. The column has a
	// `>= 1` CHECK, so zero means "nobody set this", not "version zero".
	ErrInvalidKeyVersion = errors.New("secrets: key version must be 1 or greater")

	// ErrDuplicateKeyVersion is returned when one version is configured twice.
	// Silently keeping the last one would make which key an installation
	// encrypts with depend on the order of an environment variable.
	ErrDuplicateKeyVersion = errors.New("secrets: key version configured more than once")

	// ErrNoCurrentKey is returned when the version nominated as current is not
	// in the keyring. Booting anyway would mean sealing new secrets with a key
	// the operator did not choose.
	ErrNoCurrentKey = errors.New("secrets: the current key version is not in the keyring")

	// ErrEmptyKeyring is returned when a keyring is built with no keys.
	ErrEmptyKeyring = errors.New("secrets: keyring is empty")
)

// KeyMaterial is one version's raw key.
type KeyMaterial struct {
	Version int
	Key     []byte
}

// EncodedKey is one version's base64 key, the form it arrives from the
// environment in.
type EncodedKey struct {
	Version  int
	Material string
}

// Keyring holds every key version a process can decrypt with, and exactly one
// it encrypts with.
type Keyring struct {
	keys     map[int]*key
	current  int
	versions []int // sorted ascending; the answer to "what can this process open"
}

// NewKeyring builds a keyring from raw key material.
//
// current must name one of the supplied versions. Everything else in the list
// is a decryption-only key: it opens rows that have not been rotated yet and
// never seals anything new.
func NewKeyring(materials []KeyMaterial, current int) (*Keyring, error) {
	if len(materials) == 0 {
		return nil, ErrEmptyKeyring
	}

	r := &Keyring{keys: make(map[int]*key, len(materials))}
	for _, m := range materials {
		if _, exists := r.keys[m.Version]; exists {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateKeyVersion, m.Version)
		}
		k, err := newKey(m.Version, m.Key)
		if err != nil {
			// newKey's errors carry a length or a version, never key material.
			return nil, fmt.Errorf("key version %d: %w", m.Version, err)
		}
		r.keys[m.Version] = k
		r.versions = append(r.versions, m.Version)
	}
	sort.Ints(r.versions)

	if _, ok := r.keys[current]; !ok {
		return nil, fmt.Errorf("%w: %d (configured: %v)", ErrNoCurrentKey, current, r.versions)
	}
	r.current = current
	return r, nil
}

// NewSingleVersionKeyring builds a keyring holding exactly one key, which is
// also the current one.
//
// This is the shape of an installation that has never rotated, and it is what
// the legacy SECRETS_MASTER_KEY normalises to. Offered as its own constructor
// because "one key, and it is version 1" is the overwhelmingly common case and
// spelling it out as a slice literal at every call site obscures which tests
// are ABOUT rotation and which merely need a working cipher.
func NewSingleVersionKeyring(version int, material []byte) (*Keyring, error) {
	return NewKeyring([]KeyMaterial{{Version: version, Key: material}}, version)
}

// NewKeyringFromBase64 builds a keyring from the encoded form.
//
// This is the boundary the environment crosses. Errors name the VERSION that
// was wrong and never the material, so a malformed key produces a log line an
// operator can act on and an attacker learns nothing from.
func NewKeyringFromBase64(encoded []EncodedKey, current int) (*Keyring, error) {
	materials := make([]KeyMaterial, 0, len(encoded))
	for _, e := range encoded {
		raw, err := DecodeKey(e.Material)
		if err != nil {
			return nil, fmt.Errorf("key version %d: %w", e.Version, err)
		}
		materials = append(materials, KeyMaterial{Version: e.Version, Key: raw})
	}
	return NewKeyring(materials, current)
}

// CurrentVersion is the version every new seal is stamped with.
func (r *Keyring) CurrentVersion() int { return r.current }

// Versions lists every version this process can open, ascending. The caller
// gets a copy: a keyring is read-only once built, and handing out the backing
// array would let a caller reorder what "safe to remove" is computed from.
func (r *Keyring) Versions() []int {
	out := make([]int, len(r.versions))
	copy(out, r.versions)
	return out
}

// Has reports whether this process holds the key for a version. Used by the
// status command and the startup census to answer "can we still open the rows
// we have" without opening any.
func (r *Keyring) Has(version int) bool {
	_, ok := r.keys[version]
	return ok
}

// Seal encrypts plaintext under the CURRENT key.
//
// There is deliberately no variant that takes a version. The caller — a
// connection service, a rotator — has no business choosing which key a new
// secret is sealed with, and a parameter would be one more way for a row to end
// up stamped with a key that is on its way out.
func (r *Keyring) Seal(plaintext, aad []byte) (Sealed, error) {
	return r.keys[r.current].seal(plaintext, aad)
}

// Open decrypts a sealed value using the key its own version names.
func (r *Keyring) Open(s Sealed, aad []byte) ([]byte, error) {
	if s.Algorithm != "" && s.Algorithm != AlgorithmAESGCM {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, s.Algorithm)
	}

	k, ok := r.keys[s.KeyVersion]
	if !ok {
		// Includes version 0, which is not a version: the column defaults to 1
		// and CHECKs `>= 1`, so a zero here means the value was never read from
		// a real row. Guessing a key for it is the try-all-keys behaviour this
		// type exists to refuse, in its most tempting disguise.
		return nil, fmt.Errorf("%w: %d (configured: %v)", ErrUnknownKeyVersion, s.KeyVersion, r.versions)
	}
	return k.open(s, aad)
}

// OpenFailureReason classifies an Open error for metrics and logs.
//
// A closed vocabulary, because it becomes a Prometheus label. It reports the
// SHAPE of the failure and never the row, the version, or anything derived from
// the ciphertext.
func OpenFailureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrUnknownKeyVersion):
		return "unknown_key_version"
	case errors.Is(err, ErrUnsupportedAlgorithm):
		return "unsupported_algorithm"
	case errors.Is(err, ErrOpen):
		return "authentication_failed"
	default:
		return "other"
	}
}
