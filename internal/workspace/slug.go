package workspace

import (
	"regexp"
	"strings"
)

// maxSlugLength matches the workspaces_slug_format_check constraint, which is
// in turn the DNS label limit — the shape a slug has to fit if workspaces ever
// become subdomains. Keeping the two numbers equal means a slug the service
// accepts can never be rejected by the database.
const maxSlugLength = 63

// maxNameLength bounds the display name. Names are not identifiers and are
// never used in a URL, so this is only a guard against a client posting a
// megabyte of text into a column with no length limit.
const maxNameLength = 200

// slugPattern is character-for-character the regex in the
// workspaces_slug_format_check constraint (000002_workspaces.up.sql).
// Lowercase alphanumeric groups joined by single hyphens: no leading,
// trailing or doubled hyphen.
//
// If you change one, change the other — a mismatch turns a clean 400 into a
// constraint violation surfacing as 500.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedSlugs are names the platform needs for itself. They are refused at
// creation and, because archiving never releases a slug, they stay refused
// forever — a URL that means "the admin surface" can never come to mean
// "someone's workspace".
var reservedSlugs = map[string]bool{
	"admin":   true,
	"api":     true,
	"system":  true,
	"health":  true,
	"static":  true,
	"docs":    true,
	"swagger": true,
	"v1":      true,
}

// NormalizeSlug applies the transformations the API promises to make on a
// caller-supplied slug, and nothing more: trim surrounding whitespace, then
// lowercase.
//
// It deliberately does NOT slugify. A caller who sends `My Workspace` as a
// slug gets a validation error naming the problem, not a silently different
// identifier they never asked for. Slugification belongs to SlugFromName,
// where the input was explicitly a display name.
func NormalizeSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// SlugFromName derives a slug from a display name, used when the caller
// supplies a name and no slug.
//
// Here full slugification IS correct: the caller gave a human label and asked
// the server to pick an identifier. Any run of characters outside [a-z0-9]
// collapses to a single hyphen, and leading/trailing hyphens are trimmed, so
// the result satisfies slugPattern whenever it is non-empty.
//
// It can legitimately return "" — a name of only punctuation, or of non-Latin
// script, has no ASCII slug. The caller must treat that as "the client has to
// supply a slug explicitly", not as a server error.
func SlugFromName(name string) string {
	var b strings.Builder
	b.Grow(len(name))

	lastWasHyphen := true // leading hyphens are suppressed
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastWasHyphen = false
		default:
			if !lastWasHyphen {
				b.WriteByte('-')
				lastWasHyphen = true
			}
		}
	}

	out := strings.TrimSuffix(b.String(), "-")
	if len(out) > maxSlugLength {
		out = strings.TrimSuffix(out[:maxSlugLength], "-")
	}
	return out
}

// ValidateSlug checks an already-normalized slug against the format and the
// reserved set.
//
// Order matters: format is checked before reservation so that `Admin` reports
// the invalid-format problem it actually has (uppercase) rather than claiming
// to be reserved — the caller would fix the wrong thing. Normalized input
// makes this indistinguishable in practice, but the service normalizes first
// and the ordering keeps the function honest when called directly.
func ValidateSlug(slug string) error {
	if slug == "" || len(slug) > maxSlugLength || !slugPattern.MatchString(slug) {
		return ErrSlugInvalid
	}
	if reservedSlugs[slug] {
		return ErrSlugReserved
	}
	return nil
}

// IsReservedSlug reports whether a slug is permanently unavailable. Exported
// so a future workspace-provisioning path (or a validation endpoint) can ask
// without attempting a create.
func IsReservedSlug(slug string) bool {
	return reservedSlugs[NormalizeSlug(slug)]
}

// normalizeName trims a display name. Names keep their case and their internal
// whitespace — `Production EU` is a name, not an identifier.
func normalizeName(name string) string {
	return strings.TrimSpace(name)
}
