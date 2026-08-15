package connection

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

// maxNameLength bounds the display name, matching the workspace domain.
const maxNameLength = 200

// WorkspaceStore is the slice of the workspace repository this domain needs.
//
// One method, declared on the consumer side. internal/workspace's
// PostgresRepository satisfies it directly, so no adapter is required — and
// nothing here can reach the rest of the workspace API by accident.
type WorkspaceStore interface {
	GetByID(ctx context.Context, id string) (*workspace.Workspace, error)
}

// Service holds the connection business rules.
// AuditWriter persists a control-plane event inside the caller's transaction.
//
// Declared consumer-side and one method wide; *auditlog.Recorder satisfies it.
// It returns an error, unlike audit.Record which absorbs one — which is the
// whole of [TD-033] in one signature: an audit row this service cannot write is
// a mutation this service must not commit.
//
// [TD-033]: docs/TECH_DEBT.md#td-033
type AuditWriter interface {
	RecordTx(ctx context.Context, tx database.Tx, e audit.Event) error
}

// TargetKind is the audit resource type every event in this domain names. It
// must match a value audit_events_resource_type_check permits.
const TargetKind = "connection"

type Service struct {
	repo       Repository
	workspaces WorkspaceStore
	keyring    *secrets.Keyring
	verifier   Verifier
	tx         database.Runner
	audit      AuditWriter

	// now is injectable so tests can assert on exact timestamps and drive the
	// verification-expiry clock without sleeping.
	now func() time.Time
}

// NewService constructs a Service. It returns nil when any collaborator is
// missing, which the composition root reads as "this domain is not wired" and
// answers by omitting the routes — the same signal identity and workspace use.
//
// The Keyring is the one that is genuinely optional in deployment: no master key
// configured means no connections, because storing a provider credential
// without one is not an option worth offering.
func NewService(repo Repository, workspaces WorkspaceStore, keyring *secrets.Keyring, verifier Verifier,
	tx database.Runner, auditWriter AuditWriter) *Service {
	if repo == nil || workspaces == nil || keyring == nil || verifier == nil || tx == nil || auditWriter == nil {
		return nil
	}
	return &Service{
		repo: repo, workspaces: workspaces, keyring: keyring, verifier: verifier,
		tx: tx, audit: auditWriter, now: time.Now,
	}
}

// CreateInput is the service-level create payload.
type CreateInput struct {
	Name         string
	Provider     string
	BaseURL      string
	Realm        string
	ClientID     string
	ClientSecret string
}

// UpdateInput carries the editable fields. Nil means "leave alone".
type UpdateInput struct {
	Name         *string
	BaseURL      *string
	Realm        *string
	ClientID     *string
	ClientSecret *string
}

// Create validates the input, seals the client secret, and inserts a draft.
func (s *Service) Create(ctx context.Context, workspacePublicID string, in CreateInput, ev *audit.Event) (*Connection, error) {
	ws, err := s.requireWritableWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if len(name) > maxNameLength {
		return nil, ErrInvalidRequest
	}

	provider, err := parseProvider(in.Provider)
	if err != nil {
		return nil, err
	}

	baseURL, err := normalizeBaseURL(in.BaseURL)
	if err != nil {
		return nil, err
	}

	realm := strings.TrimSpace(in.Realm)
	if realm == "" {
		return nil, ErrRealmRequired
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID == "" {
		return nil, ErrClientIDRequired
	}
	// The secret is NOT trimmed: leading or trailing whitespace could be part
	// of it, and silently altering a credential produces a failure that looks
	// like the provider's fault.
	if in.ClientSecret == "" {
		return nil, ErrClientSecretRequired
	}

	id, err := publicid.New()
	if err != nil {
		return nil, err
	}

	sealed, err := s.keyring.Seal([]byte(in.ClientSecret), secretAAD(id))
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	c := &Connection{
		ID:          id,
		WorkspaceID: ws.ID,
		Name:        name,
		Provider:    provider,
		Status:      StatusDraft,
		BaseURL:     baseURL,
		Realm:       realm,
		ClientID:    clientID,
		Health:      HealthUnknown,
		AccessMode:  AccessModeUnknown,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// The row holds a sealed provider credential, so "a connection exists that
	// nobody is recorded as having created" is the most consequential silent
	// state this domain can reach. It commits with its event or not at all.
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		if err := s.repo.WithTx(tx).Create(ctx, c, sealed); err != nil {
			return err
		}
		ev.Workspace = ws.PublicID()
		ev.Target = audit.Target{Kind: TargetKind, ID: c.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return c, nil
}

// Get returns one connection, scoped to its workspace.
func (s *Service) Get(ctx context.Context, workspacePublicID, connectionPublicID string) (*Connection, error) {
	_, c, err := s.resolve(ctx, workspacePublicID, connectionPublicID)
	return c, err
}

// List returns a workspace's connections.
func (s *Service) List(ctx context.Context, workspacePublicID, rawStatus string) ([]Connection, error) {
	ws, err := s.requireWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, err
	}
	filter, err := ParseStatusFilter(rawStatus)
	if err != nil {
		return nil, err
	}
	return s.repo.List(ctx, ws.ID, filter)
}

// Update edits a draft connection's configuration.
func (s *Service) Update(ctx context.Context, workspacePublicID, connectionPublicID string, in UpdateInput, ev *audit.Event) (*Connection, error) {
	_, c, err := s.resolve(ctx, workspacePublicID, connectionPublicID)
	if err != nil {
		return nil, err
	}
	if err := c.CanUpdate(); err != nil {
		return nil, err
	}

	patch := ConfigPatch{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, ErrNameRequired
		}
		if len(name) > maxNameLength {
			return nil, ErrInvalidRequest
		}
		patch.Name = &name
	}
	if in.BaseURL != nil {
		baseURL, err := normalizeBaseURL(*in.BaseURL)
		if err != nil {
			return nil, err
		}
		patch.BaseURL = &baseURL
	}
	if in.Realm != nil {
		realm := strings.TrimSpace(*in.Realm)
		if realm == "" {
			return nil, ErrRealmRequired
		}
		patch.Realm = &realm
	}
	if in.ClientID != nil {
		clientID := strings.TrimSpace(*in.ClientID)
		if clientID == "" {
			return nil, ErrClientIDRequired
		}
		patch.ClientID = &clientID
	}

	var sealed *secrets.Sealed
	if in.ClientSecret != nil {
		if *in.ClientSecret == "" {
			return nil, ErrClientSecretRequired
		}
		s2, err := s.keyring.Seal([]byte(*in.ClientSecret), secretAAD(c.ID))
		if err != nil {
			return nil, err
		}
		sealed = &s2
	}

	var updated *Connection
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		updated, err = s.repo.WithTx(tx).UpdateConfig(ctx, c.ID, patch, sealed, s.now().UTC())
		if err != nil {
			return err
		}
		if updated == nil {
			return ErrNotFound
		}
		ev.Target = audit.Target{Kind: TargetKind, ID: updated.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return updated, nil
}

// Verify probes the provider and records the verdict.
//
// The report is returned alongside the updated connection so the caller can
// show an operator which specific check failed — the row only keeps the
// summary, because a per-check history is a table this slice does not need.
func (s *Service) Verify(ctx context.Context, workspacePublicID, connectionPublicID string, ev *audit.Event) (*Connection, VerifyReport, error) {
	_, c, err := s.resolve(ctx, workspacePublicID, connectionPublicID)
	if err != nil {
		return nil, VerifyReport{}, err
	}

	sealed, err := s.repo.OpenSecret(ctx, c.ID)
	if err != nil {
		return nil, VerifyReport{}, err
	}
	if sealed == nil {
		return nil, VerifyReport{}, ErrNotFound
	}

	plaintext, err := s.keyring.Open(*sealed, secretAAD(c.ID))
	if err != nil {
		// The stored secret cannot be opened: wrong master key, a rotated key,
		// or a tampered row. This is an operator emergency, not a probe result,
		// so it is an internal error rather than an unhealthy verdict — marking
		// the connection unhealthy would suggest the provider is at fault.
		return nil, VerifyReport{}, err
	}

	report := s.verifier.Verify(ctx, VerifyTarget{
		BaseURL:      c.BaseURL,
		Realm:        c.Realm,
		ClientID:     c.ClientID,
		ClientSecret: string(plaintext),
	})

	// ─── What is and is not atomic here ─────────────────────────────────────
	//
	// The provider probe above already ran, and rolling this transaction back
	// does NOT un-run it. That is fine and is worth being precise about: the
	// probe is a READ — it authenticates, reads the realm and lists users — so
	// there is no external state to undo. What must be atomic is the pair that
	// lives in PostgreSQL: the recorded verdict, and the record that a
	// verification happened.
	//
	// A rolled-back verification therefore means the connection keeps its
	// PREVIOUS verdict, which is the safe direction: an operator re-runs verify.
	var updated *Connection
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		updated, err = s.repo.WithTx(tx).SaveVerification(ctx, c.ID, report)
		if err != nil {
			return err
		}
		if updated == nil {
			return ErrNotFound
		}
		ev.Target = audit.Target{Kind: TargetKind, ID: updated.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, VerifyReport{}, err
	}
	return updated, report, nil
}

// Activate promotes a verified draft, retiring the workspace's previous active
// connection in the same transaction.
func (s *Service) Activate(ctx context.Context, workspacePublicID, connectionPublicID string, ev *audit.Event) (*Connection, error) {
	ws, c, err := s.resolve(ctx, workspacePublicID, connectionPublicID)
	if err != nil {
		return nil, err
	}
	// An archived workspace is frozen: activating a connection inside one would
	// bring a provider into service for a context nobody is administering.
	if ws.IsArchived() {
		return nil, ErrWorkspaceArchived
	}
	if err := c.CanActivate(s.now().UTC()); err != nil {
		return nil, err
	}

	// ─── Two row updates and an audit row, all or nothing ───────────────────
	//
	// Activate retires the incumbent AND promotes the successor. Those were
	// already one transaction, because an interleaving that did one without the
	// other leaves a workspace with no active connection or trips the partial
	// unique index. Slice 15 puts the audit row inside the SAME transaction, so
	// a failed audit write now rolls back BOTH updates rather than one.
	//
	// That matters more here than anywhere else in the control plane: activation
	// silently redirects every subsequent identity operation for the workspace
	// to a different realm, and it is the one change no other event can be
	// reconstructed from.
	var activated *Connection
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		activated, err = s.repo.WithTx(tx).Activate(ctx, c.ID, c.WorkspaceID, s.now().UTC())
		if err != nil {
			return err
		}
		if activated == nil {
			return ErrNotFound
		}
		ev.Target = audit.Target{Kind: TargetKind, ID: activated.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return activated, nil
}

// Retire moves a connection to its terminal state.
func (s *Service) Retire(ctx context.Context, workspacePublicID, connectionPublicID string, ev *audit.Event) (*Connection, error) {
	_, c, err := s.resolve(ctx, workspacePublicID, connectionPublicID)
	if err != nil {
		return nil, err
	}
	if err := c.CanRetire(); err != nil {
		return nil, err
	}

	var retired *Connection
	if err := s.tx.InTx(ctx, func(tx database.Tx) error {
		var err error
		retired, err = s.repo.WithTx(tx).Retire(ctx, c.ID, s.now().UTC())
		if err != nil {
			return err
		}
		if retired == nil {
			return ErrNotFound
		}
		ev.Target = audit.Target{Kind: TargetKind, ID: retired.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	}); err != nil {
		return nil, err
	}
	return retired, nil
}

// Delete removes a draft or retired connection, and with it the sealed
// credential. Active connections must be retired first.
func (s *Service) Delete(ctx context.Context, workspacePublicID, connectionPublicID string, ev *audit.Event) error {
	_, c, err := s.resolve(ctx, workspacePublicID, connectionPublicID)
	if err != nil {
		return err
	}
	if err := c.CanDelete(); err != nil {
		return err
	}

	// The one control-plane mutation that DESTROYS a row rather than
	// transitioning it, which makes the audit event the only remaining evidence
	// the connection ever existed. It must not be the half that fails.
	return s.tx.InTx(ctx, func(tx database.Tx) error {
		if err := s.repo.WithTx(tx).Delete(ctx, c.ID); err != nil {
			return err
		}
		ev.Target = audit.Target{Kind: TargetKind, ID: c.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// resolve validates both ids, loads both rows, and confirms the connection
// belongs to the workspace in the path.
//
// The ownership check is what makes the nested route meaningful: without it,
// /v1/workspaces/{A}/connections/{B} would happily operate on a connection
// belonging to a different workspace, and the path would be decoration. A
// mismatch is reported as ErrNotFound, not as a distinct error — from the
// caller's position that connection does not exist under that workspace, and
// saying more would confirm it exists somewhere else.
func (s *Service) resolve(ctx context.Context, workspacePublicID, connectionPublicID string) (*workspace.Workspace, *Connection, error) {
	ws, err := s.requireWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, nil, err
	}

	id, err := publicid.Parse(publicid.ConnectionPrefix, connectionPublicID)
	if err != nil {
		return nil, nil, ErrInvalidID
	}

	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || c.WorkspaceID != ws.ID {
		return nil, nil, ErrNotFound
	}
	return ws, c, nil
}

// requireWorkspace loads the workspace named in the path, archived or not.
func (s *Service) requireWorkspace(ctx context.Context, workspacePublicID string) (*workspace.Workspace, error) {
	id, err := publicid.Parse(publicid.WorkspacePrefix, workspacePublicID)
	if err != nil {
		return nil, ErrInvalidWorkspaceID
	}

	ws, err := s.workspaces.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, ErrWorkspaceNotFound
	}
	return ws, nil
}

// requireWritableWorkspace additionally refuses an archived workspace.
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

// secretAAD binds a sealed credential to the connection row it belongs to, so
// a ciphertext cannot be moved between connections.
func secretAAD(connectionID string) []byte {
	return secrets.AAD("connection", connectionID, "client_secret")
}

// parseProvider accepts the empty string as "the default", since one provider
// exists and requiring callers to name it buys nothing today.
func parseProvider(raw string) (Provider, error) {
	switch strings.TrimSpace(raw) {
	case "", string(ProviderKeycloak):
		return ProviderKeycloak, nil
	default:
		return "", ErrProviderUnsupported
	}
}

// normalizeBaseURL validates and canonicalizes the provider URL.
//
// Only absolute http/https URLs are accepted, and the trailing slash is
// stripped so the verifier can concatenate paths without producing a double
// slash. Rejecting a relative or scheme-less URL here means the verifier's
// transport error never has to explain what an operator actually typed wrong.
func normalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrBaseURLInvalid
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", ErrBaseURLInvalid
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrBaseURLInvalid
	}
	if u.Host == "" {
		return "", ErrBaseURLInvalid
	}
	return strings.TrimRight(trimmed, "/"), nil
}
