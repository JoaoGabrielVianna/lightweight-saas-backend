package project

import (
	"context"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

// Authenticator resolves an opaque project credential into a principal.
//
// It implements auth.ProjectAuthenticator. The interface is declared in
// internal/auth and implemented here for the same reason auth.AdminChecker is:
// the middleware needs a one-method contract, and the package that owns the
// storage is the one that can satisfy it. Declaring it the other way round
// would make auth import project and project import auth.
//
// It performs NO authorization. It answers "which project and credential is
// this, and may it authenticate at all?" — never "may it do X". The workspace
// binding it returns is what internal/authz later compares against the request
// path.
type Authenticator struct {
	repo Repository
	now  func() time.Time
}

// NewAuthenticator constructs an Authenticator. Returns nil when the repository
// is missing, so the composition root omits the project surface rather than
// wiring a middleware that would panic.
func NewAuthenticator(repo Repository) *Authenticator {
	if repo == nil {
		return nil
	}
	return &Authenticator{repo: repo, now: time.Now}
}

// AuthenticateCredential resolves a token.
//
// Returns (nil, nil) for every unusable credential, whatever the cause:
// malformed token, unknown prefix, wrong secret, revoked, expired, or the
// project archived. The caller cannot tell them apart, and that is the design —
// a public response that distinguished them would let an attacker confirm which
// prefixes exist and which keys were merely revoked.
//
// Returns an error ONLY when the answer could not be determined, which is a
// 503 upstream rather than a 401. Telling a correctly configured backend that
// its credential is invalid during a database outage would send an operator
// rotating keys that were never the problem.
//
// # Cost and shape
//
//	parse locally      → no I/O for malformed input
//	one indexed SELECT → credential + project together
//	constant-time hash comparison, ALWAYS
//
// The comparison runs even when the lookup found nothing, against a fixed dummy
// digest. Without that, an unknown prefix would return measurably faster than a
// known prefix with a wrong secret, which is a prefix-enumeration oracle.
func (a *Authenticator) AuthenticateCredential(ctx context.Context, token string) (*auth.ProjectPrincipal, error) {
	parsed, ok := parseToken(token)
	if !ok {
		// Malformed input never reaches the database. This is both a cost
		// property and a denial-of-service one: arbitrary garbage cannot be
		// turned into query load.
		return nil, nil
	}

	cred, proj, err := a.repo.FindByKeyPrefix(ctx, parsed.lookup)
	if err != nil {
		return nil, err
	}
	if cred == nil || proj == nil {
		compareAgainstDummy(parsed.secret)
		return nil, nil
	}

	if !secretMatches(parsed.secret, cred.KeyHash) {
		return nil, nil
	}

	now := a.now().UTC()
	if !cred.IsUsable(now) {
		return nil, nil
	}
	// Archiving a project stops every one of its credentials here, in the same
	// row fetch, with no per-credential write anywhere.
	if proj.IsArchived() {
		return nil, nil
	}

	// last_used_at is operational metadata, not part of the decision. Its
	// failure must never invalidate an authentication that has already been
	// proven, so the error is logged and dropped.
	if cred.NeedsLastUsedTouch(now) {
		if err := a.repo.TouchLastUsed(ctx, cred.ID, now); err != nil {
			log.Warn("could not record credential last_used_at: " + err.Error())
		}
	}

	return &auth.ProjectPrincipal{
		ProjectID:    proj.PublicID(),
		ProjectName:  proj.Name,
		CredentialID: cred.PublicID(),
		WorkspaceID:  proj.WorkspacePublicID(),
		Scopes:       cred.Scopes,
	}, nil
}
