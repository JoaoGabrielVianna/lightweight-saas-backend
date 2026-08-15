package project

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

var log = logger.New("project")

// WorkspaceStore is the slice of the workspace domain this package needs.
//
// One method, declared consumer-side, exactly as identityruntime declares its
// own. It exists so a project can be refused inside an archived or absent
// workspace without this package depending on the whole workspace service.
type WorkspaceStore interface {
	GetByID(ctx context.Context, id string) (*workspace.Workspace, error)
}

// Service owns the project and credential rules.
//
// Everything a caller may do to a project passes through here, including the
// two rules that matter most: a credential is minted with the plaintext
// returned exactly once and never stored, and every operation is scoped to the
// workspace in the path so a project id from another workspace reads as absent.
//
// Since Slice 15 it also owns the TRANSACTION BOUNDARY for its mutations, so a
// project or a credential and the record of its creation commit together or
// not at all ([TD-033]).
//
// [TD-033]: docs/TECH_DEBT.md#td-033
type Service struct {
	repo       Repository
	workspaces WorkspaceStore
	tx         database.Runner
	audit      AuditWriter
	now        func() time.Time
}

// AuditWriter persists a control-plane event inside the caller's transaction.
//
// Declared consumer-side and one method wide; *auditlog.Recorder satisfies it.
// It returns an error, unlike audit.Record which absorbs one — which is the
// whole of TD-033 in one signature: an audit row this service cannot write is a
// mutation this service must not commit.
type AuditWriter interface {
	RecordTx(ctx context.Context, tx database.Tx, e audit.Event) error
}

// NewService constructs a Service. Returns nil when a collaborator is missing,
// the same "this is not wired" signal the other domains use.
//
// The runner and the writer are REQUIRED. A Service that fell back to a
// non-transactional path when they were absent would make atomicity conditional
// on wiring nobody checks, and the failure mode would be a production
// deployment that is silently non-atomic while every test still passes.
func NewService(repo Repository, workspaces WorkspaceStore, tx database.Runner, auditWriter AuditWriter) *Service {
	if repo == nil || workspaces == nil || tx == nil || auditWriter == nil {
		return nil
	}
	return &Service{repo: repo, workspaces: workspaces, tx: tx, audit: auditWriter, now: time.Now}
}

// ─── Projects ───────────────────────────────────────────────────────────────

// List returns every project in a workspace, active and archived, together with
// how many live credentials each holds.
//
// The counts come back in one grouped query rather than one per project: this
// listing is the console's landing screen for the surface, and an N+1 there
// would grow with exactly the thing an operator adds most of.
func (s *Service) List(ctx context.Context, workspacePublicID string) ([]Project, map[string]int, error) {
	ws, err := s.requireWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, nil, err
	}
	items, err := s.repo.ListProjects(ctx, ws.ID)
	if err != nil {
		log.Error("list projects: " + err.Error())
		return nil, nil, ErrInternal
	}

	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}
	counts, err := s.repo.CountActiveCredentialsByProject(ctx, ids)
	if err != nil {
		log.Error("count active credentials: " + err.Error())
		return nil, nil, ErrInternal
	}
	return items, counts, nil
}

// Create provisions a project inside a workspace.
//
// The workspace must exist and be active: creating a project inside a frozen
// workspace would mint credentials for a context where every identity operation
// is already refused.
func (s *Service) Create(ctx context.Context, workspacePublicID, name string, ev *audit.Event) (*Project, error) {
	ws, err := s.requireWritableWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, err
	}

	name = normalizeName(name)
	if name == "" || len(name) > MaxNameLength {
		return nil, ErrNameRequired
	}

	id, err := publicid.New()
	if err != nil {
		log.Error("generate project id: " + err.Error())
		return nil, ErrInternal
	}

	now := s.now().UTC()
	p := &Project{
		ID:          id,
		WorkspaceID: ws.ID,
		Name:        name,
		Status:      StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// The project row and its audit row go in together. A project that exists
	// with no record of who created it is a control-plane change nobody can
	// account for — and this project is what future credentials will belong to.
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		if err := s.repo.WithTx(tx).CreateProject(ctx, p); err != nil {
			if errors.Is(err, ErrNameTaken) {
				return ErrNameTaken
			}
			log.Error("create project: " + err.Error())
			return ErrInternal
		}
		ev.Workspace = ws.PublicID()
		ev.Target = audit.Target{Kind: TargetKindProject, ID: p.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return p, nil
}

// Get loads one project, scoped to its workspace.
func (s *Service) Get(ctx context.Context, workspacePublicID, projectPublicID string) (*Project, error) {
	_, p, err := s.resolve(ctx, workspacePublicID, projectPublicID)
	return p, err
}

// ActiveCredentialCount reports how many of a project's credentials can
// currently authenticate. Used by the single-project responses; the listing
// uses the grouped form instead.
func (s *Service) ActiveCredentialCount(ctx context.Context, p *Project) (int, error) {
	if p == nil {
		return 0, nil
	}
	n, err := s.repo.CountActiveCredentials(ctx, p.ID)
	if err != nil {
		log.Error("count active credentials: " + err.Error())
		return 0, ErrInternal
	}
	return n, nil
}

// Rename changes an active project's name.
func (s *Service) Rename(ctx context.Context, workspacePublicID, projectPublicID, name string, ev *audit.Event) (*Project, error) {
	_, p, err := s.resolve(ctx, workspacePublicID, projectPublicID)
	if err != nil {
		return nil, err
	}

	name = normalizeName(name)
	if name == "" || len(name) > MaxNameLength {
		return nil, ErrNameRequired
	}
	if p.IsArchived() {
		return nil, ErrArchived
	}

	var updated *Project
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		updated, err = s.repo.WithTx(tx).UpdateProjectName(ctx, p.ID, name, s.now().UTC())
		if err != nil {
			if errors.Is(err, ErrArchived) || errors.Is(err, ErrNameTaken) {
				return err
			}
			log.Error("rename project: " + err.Error())
			return ErrInternal
		}
		if updated == nil {
			return ErrNotFound
		}
		ev.Target = audit.Target{Kind: TargetKindProject, ID: updated.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return updated, nil
}

// Archive freezes a project. Idempotent: archiving an already-archived project
// succeeds and returns it, which is what makes a retried request safe.
//
// Credentials are not rewritten. Authentication reads the project's status in
// the same lookup as the credential, so this one UPDATE stops all of them at
// once — see ArchiveProject.
func (s *Service) Archive(ctx context.Context, workspacePublicID, projectPublicID string, ev *audit.Event) (*Project, error) {
	_, p, err := s.resolve(ctx, workspacePublicID, projectPublicID)
	if err != nil {
		return nil, err
	}

	// Archiving a project stops every one of its credentials authenticating, so
	// this is the most security-consequential row in the domain. It commits with
	// its audit event or not at all.
	var archived *Project
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		archived, err = s.repo.WithTx(tx).ArchiveProject(ctx, p.ID, s.now().UTC())
		if err != nil {
			log.Error("archive project: " + err.Error())
			return ErrInternal
		}
		if archived == nil {
			return ErrNotFound
		}
		ev.Target = audit.Target{Kind: TargetKindProject, ID: archived.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return archived, nil
}

// ─── Credentials ────────────────────────────────────────────────────────────

// CreateCredentialInput is what an operator supplies.
type CreateCredentialInput struct {
	Label     string
	Scopes    []string
	ExpiresAt *time.Time
	// CreatedBy is the operator's Keycloak subject, threaded from the handler.
	CreatedBy string
}

// CreateCredential mints a credential and returns it together with the ONE-TIME
// plaintext token.
//
// The plaintext is returned as a separate value rather than as a field on
// Credential, and that is the structural half of "the secret is shown once":
// the domain type has nowhere to carry it, so no listing, response, log line or
// audit event can include it by accident. The same guarantee
// connection.Connection makes about provider secrets.
func (s *Service) CreateCredential(ctx context.Context, workspacePublicID, projectPublicID string, in CreateCredentialInput, ev *audit.Event) (*Credential, string, error) {
	_, p, err := s.resolve(ctx, workspacePublicID, projectPublicID)
	if err != nil {
		return nil, "", err
	}
	if p.IsArchived() {
		return nil, "", ErrArchived
	}

	label := strings.TrimSpace(in.Label)
	if label == "" || len(label) > MaxLabelLength {
		return nil, "", ErrLabelRequired
	}

	scopes, bad, ok := authz.NormalizeScopes(in.Scopes)
	if !ok {
		return nil, "", invalidScopeError(bad)
	}
	// An empty scope list is refused rather than treated as "no permissions":
	// a credential that authenticates and can do nothing is a mistake worth
	// reporting at creation, not at 3am from a backend's error log.
	if len(scopes) == 0 {
		return nil, "", ErrInvalidScope
	}

	now := s.now().UTC()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return nil, "", ErrInvalidExpiry
	}

	// The cap is checked before minting so a refused create never consumes
	// entropy or leaves an orphan row.
	active, err := s.repo.CountActiveCredentials(ctx, p.ID)
	if err != nil {
		log.Error("count active credentials: " + err.Error())
		return nil, "", ErrInternal
	}
	if active >= MaxActiveCredentials {
		return nil, "", ErrCredentialLimitReached
	}

	id, err := publicid.New()
	if err != nil {
		log.Error("generate credential id: " + err.Error())
		return nil, "", ErrInternal
	}
	minted, err := MintCredential()
	if err != nil {
		// A failure to read crypto/rand is never degraded into a weaker key.
		log.Error("mint credential: " + err.Error())
		return nil, "", ErrInternal
	}

	cred := &Credential{
		ID:         id,
		ProjectID:  p.ID,
		Label:      label,
		KeyPrefix:  minted.KeyPrefix,
		KeyHash:    minted.KeyHash,
		KeyHashAlg: "sha256",
		Scopes:     authz.ScopeStrings(scopes),
		ExpiresAt:  in.ExpiresAt,
		CreatedBy:  in.CreatedBy,
		CreatedAt:  now,
	}
	// ─── The credential and its record commit together ──────────────────────
	//
	// This is the sharpest case in the domain, because the plaintext is handed
	// back exactly once and never stored. If the row committed and the audit
	// write then failed, the caller would hold a working credential that the
	// installation has no record of issuing — a live key with no provenance,
	// and no way to reconstruct which scopes it was granted or by whom.
	//
	// Rolling back means the opposite and better failure: the caller gets a 500
	// and the token they were shown never authenticates, because the row it
	// would have matched does not exist. A retry mints a different one.
	//
	// The PLAINTEXT is not in the event. `minted.Token` never leaves this
	// function except as a return value; the metadata carries the scope list,
	// which is what an operator needs when reconstructing what a leaked key
	// could do.
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		if err := s.repo.WithTx(tx).CreateCredential(ctx, cred); err != nil {
			log.Error("create credential: " + err.Error())
			return ErrInternal
		}
		ev.Target = audit.Target{Kind: TargetKindCredential, ID: cred.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, "", err
	}
	return cred, minted.Token, nil
}

// ListCredentials returns a project's credentials, including revoked ones.
func (s *Service) ListCredentials(ctx context.Context, workspacePublicID, projectPublicID string) ([]Credential, error) {
	_, p, err := s.resolve(ctx, workspacePublicID, projectPublicID)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.ListCredentials(ctx, p.ID)
	if err != nil {
		log.Error("list credentials: " + err.Error())
		return nil, ErrInternal
	}
	return items, nil
}

// RevokeCredential revokes a credential. Effective immediately: authentication
// reads the row on every request and there is no cache to invalidate.
func (s *Service) RevokeCredential(ctx context.Context, workspacePublicID, projectPublicID, credentialPublicID, revokedBy string, ev *audit.Event) (*Credential, error) {
	_, p, err := s.resolve(ctx, workspacePublicID, projectPublicID)
	if err != nil {
		return nil, err
	}

	credID, err := publicid.Parse(publicid.CredentialPrefix, credentialPublicID)
	if err != nil {
		return nil, ErrInvalidCredentialID
	}

	existing, err := s.repo.GetCredential(ctx, credID)
	if err != nil {
		log.Error("load credential: " + err.Error())
		return nil, ErrInternal
	}
	// A credential belonging to another project reads as absent, so a caller
	// cannot use this endpoint to confirm that a credential id exists elsewhere.
	if existing == nil || existing.ProjectID != p.ID {
		return nil, ErrCredentialNotFound
	}

	// ─── Revocation has a consequence worth stating plainly ─────────────────
	//
	// If the audit write fails, the revocation is rolled back — which means an
	// operator who saw a 500 must assume THE CREDENTIAL IS STILL LIVE and retry.
	// That is the honest behaviour and it is deliberately not the convenient
	// one: the alternative is to commit the revocation and return an error,
	// which tells an operator their kill switch failed while it has in fact
	// fired. Acting on a false negative ("it did not work, keep the key in the
	// deployment") is recoverable; acting on a false positive is not.
	//
	// docs/AUDIT.md §7 states this for operators, and the API documentation
	// says a 500 here means the state is unchanged.
	var revoked *Credential
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		revoked, err = s.repo.WithTx(tx).RevokeCredential(ctx, credID, revokedBy, s.now().UTC())
		if err != nil {
			if errors.Is(err, ErrCredentialAlreadyRevoked) {
				return ErrCredentialAlreadyRevoked
			}
			log.Error("revoke credential: " + err.Error())
			return ErrInternal
		}
		if revoked == nil {
			return ErrCredentialNotFound
		}
		ev.Target = audit.Target{Kind: TargetKindCredential, ID: revoked.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return revoked, nil
}

// ─── Shared resolution ──────────────────────────────────────────────────────

// resolve loads the workspace and the project, and confirms the project belongs
// to that workspace.
//
// The ownership check is what makes project ids workspace-scoped in practice: a
// valid `prj_` id from another workspace returns ErrNotFound, identical to an
// id that never existed. Without it, an operator of one workspace could confirm
// the existence of another's projects by id.
func (s *Service) resolve(ctx context.Context, workspacePublicID, projectPublicID string) (*workspace.Workspace, *Project, error) {
	ws, err := s.requireWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, nil, err
	}

	id, err := publicid.Parse(publicid.ProjectPrefix, projectPublicID)
	if err != nil {
		return nil, nil, ErrInvalidID
	}

	p, err := s.repo.GetProject(ctx, id)
	if err != nil {
		log.Error("load project: " + err.Error())
		return nil, nil, ErrInternal
	}
	if p == nil || p.WorkspaceID != ws.ID {
		return nil, nil, ErrNotFound
	}
	return ws, p, nil
}

func (s *Service) requireWorkspace(ctx context.Context, workspacePublicID string) (*workspace.Workspace, error) {
	id, err := publicid.Parse(publicid.WorkspacePrefix, workspacePublicID)
	if err != nil {
		return nil, ErrInvalidWorkspaceID
	}
	ws, err := s.workspaces.GetByID(ctx, id)
	if err != nil {
		log.Error("load workspace: " + err.Error())
		return nil, ErrInternal
	}
	if ws == nil {
		return nil, ErrWorkspaceNotFound
	}
	return ws, nil
}

func (s *Service) requireWritableWorkspace(ctx context.Context, workspacePublicID string) (*workspace.Workspace, error) {
	ws, err := s.requireWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, err
	}
	if ws.IsArchived() {
		return nil, ErrWorkspaceArchived
	}
	return ws, nil
}

// normalizeName trims surrounding whitespace and collapses internal runs, so
// "Billing  worker" and "Billing worker" cannot both exist and be
// indistinguishable in a list.
func normalizeName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

// The audit resource kinds this domain names. Both must match values the
// audit_events_resource_type_check constraint permits, which
// TestAuditKinds_MatchTheDatabaseConstraint pins.
const (
	TargetKindProject    = "project"
	TargetKindCredential = "project_credential"
)
