package connection

import (
	"net/http"
	"testing"
	"time"
)

// The row ↔ domain conversion is what keeps GORM's tags and zero-value
// semantics out of the service and the handler. It is pure, so it is tested
// here rather than only through the integration suite — a mapping bug would
// otherwise surface as a mysteriously empty field far from its cause.

func TestRow_TableName(t *testing.T) {
	if got := (row{}).TableName(); got != "connections" {
		t.Errorf("TableName = %q, want connections", got)
	}
}

func TestRow_ToDomain(t *testing.T) {
	verifiedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	activatedAt := verifiedAt.Add(time.Minute)
	retiredAt := verifiedAt.Add(2 * time.Minute)
	message := "all good"

	r := row{
		ID:             fixtureConnID,
		WorkspaceID:    fixtureWorkspaceID,
		Name:           "Prod",
		Provider:       "keycloak",
		Status:         "retired",
		BaseURL:        "https://kc.example.com",
		Realm:          "saas",
		ClientID:       "svc",
		Health:         "healthy",
		HealthMessage:  &message,
		AccessMode:     "full",
		LastVerifiedAt: &verifiedAt,
		CreatedAt:      verifiedAt,
		UpdatedAt:      retiredAt,
		ActivatedAt:    &activatedAt,
		RetiredAt:      &retiredAt,
	}

	got := r.toDomain()

	if got.ID != fixtureConnID || got.WorkspaceID != fixtureWorkspaceID {
		t.Errorf("ids = %q / %q", got.ID, got.WorkspaceID)
	}
	if got.Provider != ProviderKeycloak || got.Status != StatusRetired {
		t.Errorf("provider/status = %q / %q", got.Provider, got.Status)
	}
	if got.Health != HealthHealthy || got.AccessMode != AccessModeFull {
		t.Errorf("health/mode = %q / %q", got.Health, got.AccessMode)
	}
	if got.HealthMessage != "all good" {
		t.Errorf("health_message = %q", got.HealthMessage)
	}
	if got.LastVerifiedAt == nil || !got.LastVerifiedAt.Equal(verifiedAt) {
		t.Errorf("last_verified_at = %v", got.LastVerifiedAt)
	}
	if got.ActivatedAt == nil || got.RetiredAt == nil {
		t.Errorf("activated_at/retired_at = %v / %v", got.ActivatedAt, got.RetiredAt)
	}
}

// TestRow_ToDomainNullHealthMessage covers the nullable column: SQL NULL must
// become "", not a nil dereference.
func TestRow_ToDomainNullHealthMessage(t *testing.T) {
	got := (&row{Status: "draft", Health: "unknown", HealthMessage: nil}).toDomain()
	if got.HealthMessage != "" {
		t.Errorf("health_message = %q, want empty for a NULL column", got.HealthMessage)
	}
	if got.LastVerifiedAt != nil || got.ActivatedAt != nil || got.RetiredAt != nil {
		t.Error("NULL timestamps must stay nil")
	}
}

// TestRowFrom_RoundTrip pins that the two conversions agree. A field added to
// one and not the other is exactly the bug this catches.
func TestRowFrom_RoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	original := &Connection{
		ID:             fixtureConnID,
		WorkspaceID:    fixtureWorkspaceID,
		Name:           "Prod",
		Provider:       ProviderKeycloak,
		Status:         StatusActive,
		BaseURL:        "https://kc.example.com",
		Realm:          "saas",
		ClientID:       "svc",
		Health:         HealthHealthy,
		AccessMode:     AccessModeLimited,
		LastVerifiedAt: &at,
		CreatedAt:      at,
		UpdatedAt:      at,
		ActivatedAt:    &at,
	}

	got := rowFrom(original).toDomain()

	if got.ID != original.ID || got.WorkspaceID != original.WorkspaceID ||
		got.Name != original.Name || got.Provider != original.Provider ||
		got.Status != original.Status || got.BaseURL != original.BaseURL ||
		got.Realm != original.Realm || got.ClientID != original.ClientID ||
		got.Health != original.Health || got.AccessMode != original.AccessMode {
		t.Errorf("round trip lost a field:\n got %+v\nwant %+v", got, original)
	}
	if got.LastVerifiedAt == nil || !got.LastVerifiedAt.Equal(at) {
		t.Errorf("last_verified_at = %v, want %v", got.LastVerifiedAt, at)
	}
	if got.ActivatedAt == nil || !got.ActivatedAt.Equal(at) {
		t.Errorf("activated_at = %v, want %v", got.ActivatedAt, at)
	}
}

// TestError_UsesTheCode pins that a wrapped chain printed into a log stays
// greppable by the same token the client received.
func TestError_UsesTheCode(t *testing.T) {
	if got := ErrNotFound.Error(); got != "connection_not_found" {
		t.Errorf("Error() = %q, want the stable code", got)
	}
	if got := immutableFieldError("slug"); got.Code != ErrInvalidRequest.Code {
		t.Errorf("immutableFieldError code = %q, want %q", got.Code, ErrInvalidRequest.Code)
	} else if got.Message == ErrInvalidRequest.Message {
		t.Error("immutableFieldError should sharpen the message while keeping the code")
	}
}

// TestErrorCatalogue_IsWellFormed guards the whole catalogue at once: a new
// entry with an empty code, an empty message or a zero status would otherwise
// only surface when a client hit that exact path.
func TestErrorCatalogue_IsWellFormed(t *testing.T) {
	catalogue := map[string]*Error{
		"ErrNotFound":             ErrNotFound,
		"ErrInvalidID":            ErrInvalidID,
		"ErrNotVerified":          ErrNotVerified,
		"ErrVerificationExpired":  ErrVerificationExpired,
		"ErrAlreadyActive":        ErrAlreadyActive,
		"ErrWorkspaceHasActive":   ErrWorkspaceHasActive,
		"ErrRetired":              ErrRetired,
		"ErrActiveCannotDelete":   ErrActiveCannotDelete,
		"ErrNotDraft":             ErrNotDraft,
		"ErrNameRequired":         ErrNameRequired,
		"ErrBaseURLInvalid":       ErrBaseURLInvalid,
		"ErrRealmRequired":        ErrRealmRequired,
		"ErrClientIDRequired":     ErrClientIDRequired,
		"ErrClientSecretRequired": ErrClientSecretRequired,
		"ErrProviderUnsupported":  ErrProviderUnsupported,
		"ErrInvalidStatusFilter":  ErrInvalidStatusFilter,
		"ErrWorkspaceNotFound":    ErrWorkspaceNotFound,
		"ErrWorkspaceArchived":    ErrWorkspaceArchived,
		"ErrInvalidWorkspaceID":   ErrInvalidWorkspaceID,
		"ErrInvalidRequest":       ErrInvalidRequest,
		"ErrInternal":             ErrInternal,
	}

	seenCodes := map[string]string{}
	for name, err := range catalogue {
		if err.Code == "" {
			t.Errorf("%s has an empty code", name)
		}
		if err.Message == "" {
			t.Errorf("%s has an empty message", name)
		}
		if err.Status < 400 || err.Status > 599 {
			t.Errorf("%s has status %d, want a 4xx or 5xx", name, err.Status)
		}
		if prev, ok := seenCodes[err.Code]; ok {
			t.Errorf("%s and %s share the code %q — codes are the client contract and must be unique",
				name, prev, err.Code)
		}
		seenCodes[err.Code] = name
	}

	// Only internal_error may be a 5xx: everything else is the caller's doing,
	// and a domain rule answered with a 500 would page the wrong person.
	for name, err := range catalogue {
		if err.Status >= http.StatusInternalServerError && err.Code != ErrInternal.Code {
			t.Errorf("%s is a %d; only internal_error may be a 5xx", name, err.Status)
		}
	}
}
