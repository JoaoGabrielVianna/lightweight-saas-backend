package publicid

import (
	"errors"
	"strings"
	"testing"
)

const sampleUUID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

// TestNew_ProducesParseableVersion4UUIDs pins the generator's output shape.
// A malformed id here would be rejected by the workspaces_slug_format_check's
// sibling — the uuid column type — only at INSERT time, i.e. after the caller
// already believed the id was valid.
func TestNew_ProducesParseableVersion4UUIDs(t *testing.T) {
	seen := make(map[string]bool, 64)

	for i := 0; i < 64; i++ {
		id, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, ok := normalizeUUID(id); !ok {
			t.Fatalf("New produced a non-canonical UUID: %q", id)
		}
		// Version nibble (index 14) must be '4'; variant nibble (index 19)
		// must be one of 8/9/a/b. These are the two bits New sets by hand, so
		// they are exactly what a refactor could break.
		if id[14] != '4' {
			t.Errorf("version nibble = %q, want '4' (id %q)", id[14], id)
		}
		if !strings.ContainsRune("89ab", rune(id[19])) {
			t.Errorf("variant nibble = %q, want one of 8/9/a/b (id %q)", id[19], id)
		}
		if id != strings.ToLower(id) {
			t.Errorf("New must emit lowercase, got %q", id)
		}
		if seen[id] {
			t.Fatalf("New returned a duplicate id %q — the generator is not random", id)
		}
		seen[id] = true
	}
}

// TestFormat_AppliesPrefix covers the response-path rendering.
func TestFormat_AppliesPrefix(t *testing.T) {
	if got, want := Format(WorkspacePrefix, sampleUUID), "ws_"+sampleUUID; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
	// The package must not be workspace-specific: a different prefix is just a
	// different argument. This is what makes conn_/prj_ free later.
	if got, want := Format("conn", sampleUUID), "conn_"+sampleUUID; got != want {
		t.Errorf("Format with another prefix = %q, want %q", got, want)
	}
}

// TestParse_Accepts covers both admitted input forms.
func TestParse_Accepts(t *testing.T) {
	tests := map[string]struct {
		in   string
		want string
	}{
		"prefixed":            {"ws_" + sampleUUID, sampleUUID},
		"bare uuid":           {sampleUUID, sampleUUID},
		"surrounding spaces":  {"  ws_" + sampleUUID + "  ", sampleUUID},
		"uppercase uuid":      {"ws_" + strings.ToUpper(sampleUUID), sampleUUID},
		"uppercase bare uuid": {strings.ToUpper(sampleUUID), sampleUUID},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(WorkspacePrefix, tc.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Parse(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParse_WrongPrefix is the case the prefix exists for: a well-formed id
// that names a different kind of object. It must be distinguishable from
// "malformed", and must never be reported to a client as "not found" — that
// would leak whether some *other* object with that UUID exists.
func TestParse_WrongPrefix(t *testing.T) {
	for _, in := range []string{
		"conn_" + sampleUUID,
		"prj_" + sampleUUID,
		"_" + sampleUUID,
		"WS_" + sampleUUID, // prefix comparison is case-sensitive
	} {
		got, err := Parse(WorkspacePrefix, in)
		if !errors.Is(err, ErrWrongPrefix) {
			t.Errorf("Parse(%q) error = %v, want ErrWrongPrefix", in, err)
		}
		if got != "" {
			t.Errorf("Parse(%q) returned %q alongside an error; must return empty", in, got)
		}
	}
}

// TestParse_Malformed covers everything that is not a UUID. Each case must be
// rejected here, before any query runs.
func TestParse_Malformed(t *testing.T) {
	tests := map[string]string{
		"empty":                "",
		"whitespace only":      "   ",
		"prefix with no uuid":  "ws_",
		"prefix only":          "ws",
		"not a uuid":           "ws_not-a-uuid",
		"double prefix":        "ws_ws_" + sampleUUID,
		"missing hyphens":      "ws_3f2504e04f8941d39a0c0305e82c3301",
		"braced form":          "ws_{" + sampleUUID + "}",
		"urn form":             "urn:uuid:" + sampleUUID,
		"too short":            "ws_3f2504e0-4f89-41d3-9a0c-0305e82c330",
		"too long":             "ws_3f2504e0-4f89-41d3-9a0c-0305e82c33011",
		"non-hex character":    "ws_3f2504e0-4f89-41d3-9a0c-0305e82c330g",
		"hyphen in wrong slot": "ws_3f2504e04-f89-41d3-9a0c-0305e82c3301",
		"sql injection-ish":    "ws_' OR 1=1 --",
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(WorkspacePrefix, in)
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("Parse(%q) error = %v, want ErrMalformed", in, err)
			}
			if got != "" {
				t.Errorf("Parse(%q) returned %q alongside an error; must return empty", in, got)
			}
		})
	}
}

// TestParse_RoundTripsFormat is the property that matters most: whatever the
// API renders, the API must accept back. A change to either side alone fails
// this.
func TestParse_RoundTripsFormat(t *testing.T) {
	for i := 0; i < 32; i++ {
		id, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		got, err := Parse(WorkspacePrefix, Format(WorkspacePrefix, id))
		if err != nil {
			t.Fatalf("round trip of %q: %v", id, err)
		}
		if got != id {
			t.Errorf("round trip = %q, want %q", got, id)
		}
	}
}
