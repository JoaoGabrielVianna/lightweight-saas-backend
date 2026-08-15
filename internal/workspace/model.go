// Package workspace owns the Workspace domain: an isolated administrative
// context inside one installation.
//
// A Workspace will later hold a Connection to an identity provider. It holds
// nothing yet and performs no Keycloak operation — this package is pure
// domain plus its own persistence and HTTP surface, and depends on neither
// internal/identity nor internal/auth.
//
// File layout follows the project's per-domain convention:
//
//	model.go      the domain type and its invariants
//	slug.go       slug normalization, validation and the reserved set
//	errors.go     the stable error codes the /v1 surface promises
//	dto.go        wire types
//	repository.go persistence (the only file that knows GORM exists)
//	service.go    business rules
//	handler.go    HTTP
package workspace

import (
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
)

// Status is the workspace lifecycle state. The set is closed, and closed in
// two places: here, and in the workspaces_status_check constraint. Adding a
// state means a migration, not just a constant.
type Status string

const (
	// StatusActive is the state every workspace is created in.
	StatusActive Status = "active"

	// StatusArchived is terminal for this slice: there is no reactivation
	// path, and the slug is never released.
	StatusArchived Status = "archived"
)

// Workspace is the domain model.
//
// It carries no GORM tags and this file imports no persistence package: the
// row type that talks to the database lives in repository.go and converts in
// both directions. The separation is what lets a schema detail change without
// reaching the service, the handler, or the wire format.
//
// ID is the canonical lowercase UUID as stored. The prefixed `ws_<uuid>` form
// exists only on the wire — see PublicID.
type Workspace struct {
	ID         string
	Slug       string
	Name       string
	Status     Status
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

// PublicID renders the identifier clients see. The bare UUID is never exposed
// by the API, so every response path goes through here.
func (w *Workspace) PublicID() string {
	return publicid.Format(publicid.WorkspacePrefix, w.ID)
}

// IsArchived reports whether the workspace is in its terminal state.
//
// It reads Status rather than ArchivedAt because Status is the field the
// database constrains and the API filters on; the two cannot disagree
// (workspaces_archived_at_check), so this is a matter of naming the source of
// truth rather than of correctness.
func (w *Workspace) IsArchived() bool {
	return w.Status == StatusArchived
}
