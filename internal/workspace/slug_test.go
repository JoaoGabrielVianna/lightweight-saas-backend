package workspace

import (
	"errors"
	"strings"
	"testing"
)

// TestNormalizeSlug_TrimsAndLowercasesOnly pins the deliberate narrowness of
// normalization. A caller-supplied slug is cleaned up, never rewritten: if
// someone sends "My Workspace" as a slug they get a validation error naming
// the problem, not a silently different identifier.
func TestNormalizeSlug_TrimsAndLowercasesOnly(t *testing.T) {
	tests := map[string]string{
		"production":       "production",
		"  production  ":   "production",
		"PRODUCTION":       "production",
		"Production-EU":    "production-eu",
		"\tstaging\n":      "staging",
		"":                 "",
		"   ":              "",
		"My Workspace":     "my workspace",     // space survives → ValidateSlug rejects
		"under_score":      "under_score",      // underscore survives → rejected
		"trailing-hyphen-": "trailing-hyphen-", // rejected, not silently trimmed
	}

	for in, want := range tests {
		if got := NormalizeSlug(in); got != want {
			t.Errorf("NormalizeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateSlug_Accepts covers the forms the API promises to take.
func TestValidateSlug_Accepts(t *testing.T) {
	for _, slug := range []string{
		"a",
		"production",
		"production-eu",
		"eu-west-1",
		"team42",
		"1",
		"a-b-c-d-e-f",
		strings.Repeat("a", maxSlugLength), // exactly at the limit
	} {
		if err := ValidateSlug(slug); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", slug, err)
		}
	}
}

// TestValidateSlug_RejectsInvalidFormat pins every shape the database's
// workspaces_slug_format_check would also reject. The two must agree: a slug
// the service lets through and the constraint refuses turns a clean 400 into
// a 500.
func TestValidateSlug_RejectsInvalidFormat(t *testing.T) {
	tests := map[string]string{
		"empty":              "",
		"uppercase":          "Production",
		"internal space":     "my workspace",
		"leading hyphen":     "-production",
		"trailing hyphen":    "production-",
		"doubled hyphen":     "production--eu",
		"underscore":         "production_eu",
		"dot":                "production.eu",
		"slash":              "production/eu",
		"only hyphen":        "-",
		"too long":           strings.Repeat("a", maxSlugLength+1),
		"unicode":            "produção",
		"leading whitespace": " production",
		"sql-ish":            "prod'; DROP TABLE workspaces;--",
	}

	for name, slug := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateSlug(slug)
			if !errors.Is(err, ErrSlugInvalid) {
				t.Errorf("ValidateSlug(%q) = %v, want ErrSlugInvalid", slug, err)
			}
		})
	}
}

// TestValidateSlug_RejectsReserved covers the names the platform keeps. These
// must stay refused permanently — archiving never releases a slug, so a URL
// that means "the admin surface" can never come to mean a workspace.
func TestValidateSlug_RejectsReserved(t *testing.T) {
	reserved := []string{"admin", "api", "system", "health", "static", "docs", "swagger", "v1"}

	for _, slug := range reserved {
		err := ValidateSlug(slug)
		if !errors.Is(err, ErrSlugReserved) {
			t.Errorf("ValidateSlug(%q) = %v, want ErrSlugReserved", slug, err)
		}
		if !IsReservedSlug(slug) {
			t.Errorf("IsReservedSlug(%q) = false, want true", slug)
		}
		// Case-insensitively reserved via normalization, so `Admin` cannot
		// sneak through a path that normalizes before checking.
		if !IsReservedSlug(strings.ToUpper(slug)) {
			t.Errorf("IsReservedSlug(%q) = false, want true", strings.ToUpper(slug))
		}
	}

	// Reserved is exact-match, not prefix-match: `admin-tools` is a perfectly
	// good workspace name and must not be swept up.
	for _, slug := range []string{"admin-tools", "apis", "systemd", "v10", "docs-internal"} {
		if err := ValidateSlug(slug); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil — reservation is exact-match", slug, err)
		}
	}
}

// TestValidateSlug_FormatCheckedBeforeReservation pins the ordering. `Admin`
// has two problems; reporting the uppercase one is right, because a caller
// told "reserved" would try `Administration` instead of `admin-tools`.
func TestValidateSlug_FormatCheckedBeforeReservation(t *testing.T) {
	if err := ValidateSlug("Admin"); !errors.Is(err, ErrSlugInvalid) {
		t.Errorf("ValidateSlug(\"Admin\") = %v, want ErrSlugInvalid (format before reservation)", err)
	}
}

// TestSlugFromName_Slugifies covers derivation, where full rewriting IS
// correct because the caller supplied a display name and asked the server to
// pick an identifier.
func TestSlugFromName_Slugifies(t *testing.T) {
	tests := map[string]string{
		"Production":         "production",
		"Production EU":      "production-eu",
		"  Production  EU  ": "production-eu",
		"My   Workspace":     "my-workspace",
		"ACME, Inc.":         "acme-inc",
		"team_42":            "team-42",
		"a/b/c":              "a-b-c",
		"Hello -- World":     "hello-world",
		"---leading":         "leading",
		"trailing---":        "trailing",
		"Ünïcödé Náme":       "n-c-d-n-me",
		"2026 Q3":            "2026-q3",
	}

	for in, want := range tests {
		got := SlugFromName(in)
		if got != want {
			t.Errorf("SlugFromName(%q) = %q, want %q", in, got, want)
		}
		// The invariant that matters: whatever derivation produces must be
		// accepted by validation, or Create would fail on its own output.
		if got != "" {
			if err := ValidateSlug(got); err != nil && !errors.Is(err, ErrSlugReserved) {
				t.Errorf("SlugFromName(%q) produced %q which fails ValidateSlug: %v", in, got, err)
			}
		}
	}
}

// TestSlugFromName_UnslugifiableNames documents the honest failure: a name
// with no ASCII alphanumerics has no slug. Returning "" (rather than something
// invented) is what lets Create report slug_invalid and tell the caller to
// supply one explicitly.
func TestSlugFromName_UnslugifiableNames(t *testing.T) {
	for _, name := range []string{"...", "   ", "!!!", "—", "生产环境"} {
		if got := SlugFromName(name); got != "" {
			t.Errorf("SlugFromName(%q) = %q, want empty", name, got)
		}
	}
}

// TestSlugFromName_TruncatesToLimit pins that a very long name cannot produce
// a slug the database would refuse, and that truncation never leaves a
// trailing hyphen (which would itself be invalid).
func TestSlugFromName_TruncatesToLimit(t *testing.T) {
	long := strings.Repeat("word ", 40) // 200 chars, slugifies to 199
	got := SlugFromName(long)

	if len(got) > maxSlugLength {
		t.Errorf("SlugFromName produced %d chars, limit is %d", len(got), maxSlugLength)
	}
	if err := ValidateSlug(got); err != nil {
		t.Errorf("truncated slug %q fails validation: %v", got, err)
	}

	// Truncation landing exactly on a hyphen must not leave one dangling.
	// "aa-" at the boundary is the case: pick a name whose 63rd char is '-'.
	boundary := SlugFromName(strings.Repeat("ab ", 30))
	if strings.HasSuffix(boundary, "-") {
		t.Errorf("truncated slug %q ends in a hyphen", boundary)
	}
	if err := ValidateSlug(boundary); err != nil {
		t.Errorf("boundary slug %q fails validation: %v", boundary, err)
	}
}

// TestNormalizeName trims but preserves case and internal spacing — a name is
// a label, not an identifier.
func TestNormalizeName(t *testing.T) {
	tests := map[string]string{
		"Production":       "Production",
		"  Production  ":   "Production",
		"Production  EU":   "Production  EU",
		"\n\tProduction\t": "Production",
		"   ":              "",
		"":                 "",
	}
	for in, want := range tests {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
