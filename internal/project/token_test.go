package project

import (
	"crypto/sha256"
	"strings"
	"testing"
)

// The token format is a security contract, not a cosmetic one: its prefix is
// what the authentication middleware discriminates on, its entropy is the only
// thing standing between an attacker and a workspace, and its encoding is what
// makes parsing unambiguous.

func TestMintCredential_Format(t *testing.T) {
	m, err := MintCredential()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	if !strings.HasPrefix(m.Token, "lw_sk_") {
		t.Errorf("token %q does not carry the lw_sk_ prefix", redact(m.Token))
	}
	if got, want := len(m.Token), len("lw_sk_")+lookupLen+1+secretLen; got != want {
		t.Errorf("token length = %d, want %d", got, want)
	}
	if len(m.KeyPrefix) != lookupLen {
		t.Errorf("key prefix length = %d, want %d", len(m.KeyPrefix), lookupLen)
	}
	if len(m.KeyHash) != sha256.Size {
		t.Errorf("hash length = %d, want %d", len(m.KeyHash), sha256.Size)
	}
	// The stored prefix must be exactly the token's lookup segment, or the
	// indexed lookup would never find the row it just wrote.
	if !strings.HasPrefix(m.Token, "lw_sk_"+m.KeyPrefix+"_") {
		t.Error("stored key prefix is not the token's lookup segment")
	}
}

func TestMintCredential_TokenIsSplittableUnambiguously(t *testing.T) {
	// The separator is "_", so an alphabet containing "_" (base64url) would make
	// this ambiguous. Base32 lowercase does not.
	m, err := MintCredential()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parts := strings.Split(m.Token, "_")
	if len(parts) != 4 {
		t.Fatalf("token split into %d parts, want exactly 4 (lw, sk, lookup, secret)", len(parts))
	}
	if parts[0] != "lw" || parts[1] != "sk" {
		t.Errorf("unexpected prefix parts %q/%q", parts[0], parts[1])
	}
}

func TestMintCredential_IsUnpredictable(t *testing.T) {
	// Not a statistical test — just proof that two mints differ in BOTH
	// segments. A generator that reused either would be catastrophic and would
	// show up here immediately.
	seenLookup := map[string]bool{}
	seenToken := map[string]bool{}
	for i := 0; i < 200; i++ {
		m, err := MintCredential()
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if seenLookup[m.KeyPrefix] {
			t.Fatal("lookup segment repeated across mints")
		}
		if seenToken[m.Token] {
			t.Fatal("token repeated across mints")
		}
		seenLookup[m.KeyPrefix] = true
		seenToken[m.Token] = true
	}
}

func TestParseToken_AcceptsAMintedToken(t *testing.T) {
	m, err := MintCredential()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parsed, ok := parseToken(m.Token)
	if !ok {
		t.Fatal("a freshly minted token failed to parse")
	}
	if parsed.lookup != m.KeyPrefix {
		t.Errorf("parsed lookup = %q, want the stored prefix", parsed.lookup)
	}
	if !secretMatches(parsed.secret, m.KeyHash) {
		t.Error("parsed secret does not match the stored hash")
	}
}

func TestParseToken_RejectsMalformedInputWithoutIO(t *testing.T) {
	// Everything checkable locally is checked locally, so garbage cannot be
	// turned into database load.
	valid, err := MintCredential()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	lookup, secret := valid.KeyPrefix, strings.TrimPrefix(valid.Token, "lw_sk_"+valid.KeyPrefix+"_")

	cases := map[string]string{
		"empty":                "",
		"jwt":                  "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhIn0.sig",
		"wrong prefix":         "lw_pk_" + lookup + "_" + secret,
		"no prefix":            lookup + "_" + secret,
		"missing separator":    "lw_sk_" + lookup + secret,
		"short lookup":         "lw_sk_" + lookup[:8] + "_" + secret,
		"long lookup":          "lw_sk_" + lookup + "aa_" + secret,
		"short secret":         "lw_sk_" + lookup + "_" + secret[:10],
		"long secret":          "lw_sk_" + lookup + "_" + secret + "aa",
		"uppercase lookup":     "lw_sk_" + strings.ToUpper(lookup) + "_" + secret,
		"uppercase secret":     "lw_sk_" + lookup + "_" + strings.ToUpper(secret),
		"base64url characters": "lw_sk_" + lookup + "_" + strings.Repeat("-", secretLen),
		"digits outside b32":   "lw_sk_" + lookup + "_" + strings.Repeat("0", secretLen),
		"whitespace":           "lw_sk_" + lookup + "_" + secret[:secretLen-1] + " ",
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseToken(token); ok {
				t.Errorf("parseToken accepted malformed input (%s)", name)
			}
		})
	}
}

func TestSecretMatches_RejectsAWrongSecret(t *testing.T) {
	a, _ := MintCredential()
	b, _ := MintCredential()

	parsedB, ok := parseToken(b.Token)
	if !ok {
		t.Fatal("parse")
	}
	if secretMatches(parsedB.secret, a.KeyHash) {
		t.Fatal("one credential's secret matched another's hash")
	}
}

func TestHashSecret_IsSHA256OfTheSecretSegment(t *testing.T) {
	// Pins the stored form. Changing it silently would invalidate every
	// existing credential with no error anywhere — the failure would look like
	// every backend simultaneously holding a bad key.
	const secret = "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst"
	want := sha256.Sum256([]byte(secret))
	got := hashSecret(secret)

	if len(got) != len(want) {
		t.Fatalf("hash length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hash differs from SHA-256 of the secret segment at byte %d", i)
		}
	}
}

func TestCompareAgainstDummy_DoesNotPanicAndAcceptsNothing(t *testing.T) {
	// The dummy comparison exists so an unknown prefix costs the same work as a
	// known one with a wrong secret. It must never be a path that succeeds.
	m, _ := MintCredential()
	parsed, _ := parseToken(m.Token)
	compareAgainstDummy(parsed.secret)

	if secretMatches(parsed.secret, dummyHash[:]) {
		t.Fatal("a real secret matched the dummy hash")
	}
}

// redact renders a token for a failure message without printing the secret.
// Used by the tests themselves so a CI log never carries a working credential,
// even one generated for a test.
func redact(token string) string {
	parts := strings.SplitN(token, "_", 4)
	if len(parts) != 4 {
		return "<malformed>"
	}
	return parts[0] + "_" + parts[1] + "_" + parts[2] + "_REDACTED"
}
