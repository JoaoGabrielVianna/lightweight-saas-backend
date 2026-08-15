package connection

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Master-key rotation.
//
// # What rotation is, and what it is not
//
//	master-key rotation      the SAME provider secret, re-sealed under a new
//	                         master key. Keycloak never hears about it.
//	credential rotation      a NEW provider secret, issued by Keycloak. That is
//	                         PATCH /connections/{id} with a client_secret, and
//	                         it lives in Service.Update.
//
// Conflating them is the expensive mistake, because they need opposite things
// from the runtime: a new credential MUST evict the cached provider (it holds a
// secret that no longer works), and a re-sealed one must NOT (it holds a secret
// that is still exactly right). See the note on updated_at in rotateOne.
//
// # Why an explicit operation and not rotate-on-read
//
// Re-sealing whatever happens to be read would finish rotation without anyone
// running anything, and it would do so by turning the runtime's read path into
// a write path. That trade was declined:
//
//   - an identity request for a workspace would mutate control-plane state, so
//     a read-only replica or a degraded database turns provider resolution into
//     an error instead of a slower success;
//   - a row nobody reads never rotates, so the operator still cannot say when
//     the old key is safe to destroy — which is the entire question rotation
//     exists to answer;
//   - and the failure semantics stop being reportable: a re-seal that fails on
//     the read path has nobody to tell.
//
// So rotation is a command an operator runs, it reports what it did, and its
// exit code says whether it finished. See cmd/secrets.
//
// # Per-row transactions, not one big one
//
// Model B from the two available: each row rotates in its own transaction, a
// failure leaves that row on its old key, and a rerun resumes.
//
// The alternative — one transaction over every row — buys an all-or-nothing
// guarantee nobody needs. Rotation is idempotent and re-runnable, so "some rows
// moved" is not a state anyone has to reason about; it is just less progress.
// What the single transaction WOULD buy is a lock held on every connection row
// for the length of the run, blocking activations and credential edits across
// every workspace at once, and a single undecryptable row discarding the work
// done for all the others. Neither is worth an atomicity nobody consumes.
//
// What must still be atomic is one row's four secret columns, and that is what
// the row-level transaction is for: ciphertext, nonce, algorithm and version
// are one value in four columns, and a reader must never see three of them
// updated.

// RotationOutcome classifies what happened to one row. A closed vocabulary: it
// reaches CLI output and log lines, never a Prometheus label with a row in it.
type RotationOutcome string

const (
	// OutcomeRotated — the row was re-sealed under the current key.
	OutcomeRotated RotationOutcome = "rotated"

	// OutcomeAlreadyCurrent — the row was already on the current key and was
	// left completely untouched. Not re-encrypted: a fresh nonce for an
	// unchanged secret would make every retry look like work happened.
	OutcomeAlreadyCurrent RotationOutcome = "already_current"

	// OutcomeFailed — the row could not be rotated. It still holds its old,
	// valid ciphertext.
	OutcomeFailed RotationOutcome = "failed"
)

// RotationFailure explains one row that did not rotate.
//
// It names the connection by its PUBLIC id and the key version the row needs.
// Both are safe: the public id is in every API response and every log line
// already, and a version is an integer. Nothing derived from the ciphertext,
// the plaintext or a key appears here — this struct is printed.
type RotationFailure struct {
	// ConnectionPublicID is the `conn_` id an operator can look up.
	ConnectionPublicID string

	// KeyVersion is the version the row is sealed under.
	KeyVersion int

	// Reason is a closed vocabulary, not an error string: "missing_key_version",
	// "cannot_open", "write_lost", "read_failed", "write_failed".
	Reason string
}

// String renders one failure as an operator-facing line.
func (f RotationFailure) String() string {
	return fmt.Sprintf("%s (sealed under v%d): %s", f.ConnectionPublicID, f.KeyVersion, f.Reason)
}

// RotationReport is what a rotation run did.
type RotationReport struct {
	// Total is how many rows were examined.
	Total int

	// AlreadyCurrent is how many were on the current key and left alone.
	AlreadyCurrent int

	// Rotated is how many were re-sealed.
	Rotated int

	// Failures is one entry per row that could not be rotated.
	Failures []RotationFailure
}

// Failed is the count of rows that could not be rotated.
func (r RotationReport) Failed() int { return len(r.Failures) }

// Complete reports whether every row now uses the current key.
func (r RotationReport) Complete() bool { return len(r.Failures) == 0 }

// KeyVersionCensus counts persisted secrets by the key version they need.
//
// This is the number the whole "is it safe to destroy the old key" question
// reduces to, and it is answerable without decrypting anything: the version is
// a column.
type KeyVersionCensus struct {
	// Rows maps key version to how many connection secrets need it.
	Rows map[int]int64
}

// Versions lists the versions present, ascending.
func (c KeyVersionCensus) Versions() []int {
	out := make([]int, 0, len(c.Rows))
	for v := range c.Rows {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// Total is how many connection secrets exist.
func (c KeyVersionCensus) Total() int64 {
	var n int64
	for _, count := range c.Rows {
		n += count
	}
	return n
}

// Unopenable returns the versions the census found that the keyring cannot
// open, ascending.
//
// This is the startup check and the pre-removal check in one: a non-empty
// result means some connection in this installation is sealed with a key this
// process does not hold.
func (c KeyVersionCensus) Unopenable(ring *secrets.Keyring) []int {
	var out []int
	for _, v := range c.Versions() {
		if !ring.Has(v) {
			out = append(out, v)
		}
	}
	return out
}

// SafeToRemove returns the keyring versions no persisted row needs.
//
// The current version is never included, however empty the table is: it is
// about to be used by the next connection anyone creates.
func (c KeyVersionCensus) SafeToRemove(ring *secrets.Keyring) []int {
	var out []int
	for _, v := range ring.Versions() {
		if v == ring.CurrentVersion() {
			continue
		}
		if c.Rows[v] == 0 {
			out = append(out, v)
		}
	}
	return out
}

// Rotator re-seals persisted connection secrets under the current key.
//
// It works directly against the database rather than through Repository. That
// is deliberate: Repository is the shape the connection SERVICE needs, and
// widening it with a rotate method would put "re-seal every secret in the
// installation" one call away from request-handling code that has no business
// reaching it. A rotation is an operator action from a separate process.
type Rotator struct {
	db   *gorm.DB
	ring *secrets.Keyring
}

// NewRotator constructs a Rotator. Returns nil when a collaborator is missing,
// matching the convention the domains use for "this is not wired".
func NewRotator(db *gorm.DB, ring *secrets.Keyring) *Rotator {
	if db == nil || ring == nil {
		return nil
	}
	return &Rotator{db: db, ring: ring}
}

// CurrentVersion is the version rotation moves rows to.
func (r *Rotator) CurrentVersion() int { return r.ring.CurrentVersion() }

// Keyring exposes the ring for reporting. Read-only by construction — the
// keyring hands out copies of its version list and no key material at all.
func (r *Rotator) Keyring() *secrets.Keyring { return r.ring }

// Census counts persisted secrets by key version.
//
// One GROUP BY over an int column. It decrypts nothing, which is what makes it
// safe to run at startup and on a ticker.
func (r *Rotator) Census(ctx context.Context) (KeyVersionCensus, error) {
	var rows []struct {
		SecretKeyVersion int
		Count            int64
	}
	err := r.db.WithContext(ctx).Model(&row{}).
		Select("secret_key_version, count(*) as count").
		Group("secret_key_version").
		Scan(&rows).Error
	if err != nil {
		return KeyVersionCensus{}, err
	}

	census := KeyVersionCensus{Rows: make(map[int]int64, len(rows))}
	for _, item := range rows {
		census.Rows[item.SecretKeyVersion] = item.Count
	}
	return census, nil
}

// Plan reports what a rotation would do, without opening a single ciphertext.
//
// This is `--dry-run`, and the limit of what a dry run can honestly claim is
// worth stating: it can tell you how many rows need rotating and whether their
// key versions are configured, because both are metadata. It CANNOT tell you
// that the configured key for a version is the right one — proving that means
// running AES-GCM over the row, which is the real run. A dry run that reported
// "all clear" and then failed on wrong key material would be worse than one
// that never claimed it.
type RotationPlan struct {
	// Census is the current distribution.
	Census KeyVersionCensus

	// Current is the version rows would move to.
	Current int

	// NeedRotation is how many rows are not on the current version.
	NeedRotation int64

	// AlreadyCurrent is how many rows are already there.
	AlreadyCurrent int64

	// MissingVersions are versions rows need that the keyring does not hold.
	// Those rows cannot rotate until the key is restored.
	MissingVersions []int

	// Blocked is how many rows sit behind MissingVersions.
	Blocked int64
}

// Plan builds a RotationPlan from metadata alone.
func (r *Rotator) Plan(ctx context.Context) (RotationPlan, error) {
	census, err := r.Census(ctx)
	if err != nil {
		return RotationPlan{}, err
	}
	return PlanFrom(census, r.ring), nil
}

// PlanFrom derives the plan from a census and a keyring.
//
// Separated from Plan so the arithmetic — which is what an operator reads
// before deciding to destroy a key — is testable without a database, and so the
// same derivation serves any future caller that already holds a census.
func PlanFrom(census KeyVersionCensus, ring *secrets.Keyring) RotationPlan {
	plan := RotationPlan{
		Census:          census,
		Current:         ring.CurrentVersion(),
		AlreadyCurrent:  census.Rows[ring.CurrentVersion()],
		MissingVersions: census.Unopenable(ring),
	}
	for _, version := range census.Versions() {
		if version == plan.Current {
			continue
		}
		plan.NeedRotation += census.Rows[version]
		if !ring.Has(version) {
			plan.Blocked += census.Rows[version]
		}
	}
	return plan
}

// Rotate re-seals every persisted connection secret under the current key.
//
// Idempotent: a row already on the current version is counted and skipped, not
// re-encrypted. Resumable: a failure affects one row, and a rerun picks up
// where the last one stopped. Interruptible: a cancelled context stops the run
// between rows, never inside one, and returns the partial report alongside the
// context's error.
//
// The error return is for the run itself — the listing query failed, the
// context was cancelled. A row that could not be rotated is NOT an error; it is
// a RotationFailure in the report, because the operator needs the other rows'
// outcomes too.
func (r *Rotator) Rotate(ctx context.Context) (RotationReport, error) {
	current := r.ring.CurrentVersion()

	// Only rows that are not already current. New rows created while this runs
	// are sealed under the current key by definition, so a row that appears
	// after this snapshot needs nothing; and a row in the snapshot whose secret
	// is edited mid-run is re-read under a lock below and found current.
	var ids []string
	err := r.db.WithContext(ctx).Model(&row{}).
		Where("secret_key_version <> ?", current).
		Order("id ASC").
		Pluck("id", &ids).Error
	if err != nil {
		return RotationReport{}, err
	}

	// The already-current count comes from the census rather than from the
	// listing, so `total` means "secrets in this installation" and not "rows I
	// happened to look at".
	census, err := r.Census(ctx)
	if err != nil {
		return RotationReport{}, err
	}

	report := RotationReport{
		Total:          int(census.Total()),
		AlreadyCurrent: int(census.Rows[current]),
	}

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			// Stop between rows. Every row already committed stays rotated, and
			// a rerun resumes — which is exactly what the per-row model promises.
			return report, err
		}

		outcome, failure := r.rotateOne(ctx, id, current)
		switch outcome {
		case OutcomeRotated:
			report.Rotated++
		case OutcomeAlreadyCurrent:
			report.AlreadyCurrent++
		case OutcomeFailed:
			report.Failures = append(report.Failures, failure)
		}
	}
	return report, nil
}

// rotateOne rotates a single row inside its own transaction.
func (r *Rotator) rotateOne(ctx context.Context, id string, current int) (RotationOutcome, RotationFailure) {
	publicID := publicid.Format(publicid.ConnectionPrefix, id)
	fail := func(version int, reason string) (RotationOutcome, RotationFailure) {
		return OutcomeFailed, RotationFailure{
			ConnectionPublicID: publicID, KeyVersion: version, Reason: reason,
		}
	}

	var (
		outcome  = OutcomeFailed
		failure  RotationFailure
		observed int
	)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SELECT ... FOR UPDATE. This is the whole concurrency story, and it is
		// the database's rather than the process's on purpose: a mutex would
		// order two goroutines and do nothing about two PROCESSES, which is the
		// shape this installation grows into. It also serialises rotation
		// against the ordinary UPDATE that changes a connection's client
		// secret, which is the interleaving that would actually destroy data —
		// read the old ciphertext, let a new credential land, then write the
		// OLD plaintext back re-sealed, resurrecting a credential the operator
		// just replaced.
		var sealed struct {
			SecretCiphertext []byte
			SecretNonce      []byte
			SecretKeyVersion int
			SecretAlg        string
		}
		err := tx.Model(&row{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("secret_ciphertext", "secret_nonce", "secret_key_version", "secret_alg").
			Where("id = ?", id).
			Take(&sealed).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Deleted between the listing and now. Nothing to rotate and
			// nothing wrong: a row that no longer exists holds no secret.
			outcome = OutcomeAlreadyCurrent
			return nil
		}
		if err != nil {
			outcome, failure = fail(0, "read_failed")
			return nil
		}
		observed = sealed.SecretKeyVersion

		if sealed.SecretKeyVersion == current {
			// Already current — under the lock, so this is authoritative and
			// not a stale read. Nothing is written: re-sealing an unchanged
			// secret would produce a new nonce and ciphertext and make an
			// idempotent rerun look like work.
			outcome = OutcomeAlreadyCurrent
			return nil
		}

		plaintext, err := r.ring.Open(secrets.Sealed{
			Ciphertext: sealed.SecretCiphertext,
			Nonce:      sealed.SecretNonce,
			KeyVersion: sealed.SecretKeyVersion,
			Algorithm:  sealed.SecretAlg,
		}, secretAAD(id))
		if err != nil {
			reason := "cannot_open"
			if errors.Is(err, secrets.ErrUnknownKeyVersion) {
				// Distinguished because the remedies differ: put the key back,
				// versus the row is not what it claims to be.
				reason = "missing_key_version"
			}
			outcome, failure = fail(sealed.SecretKeyVersion, reason)
			// Return nil: the transaction has written nothing, and rolling back
			// an empty transaction with an error would turn one unreadable row
			// into a failed run.
			return nil
		}
		// The plaintext exists for the length of this closure and no longer.
		// Wiped rather than left for the collector, so a panic dump or a core
		// file taken later does not carry a provider credential in a live heap
		// buffer.
		defer wipeSecret(plaintext)

		reSealed, err := r.ring.Seal(plaintext, secretAAD(id))
		if err != nil {
			outcome, failure = fail(sealed.SecretKeyVersion, "seal_failed")
			return nil
		}

		// The version guard is belt to the lock's braces. FOR UPDATE already
		// guarantees nothing moved underneath us; the guard means that if the
		// lock is ever lost — a refactor, a different isolation level — the
		// write fails loudly instead of overwriting someone else's newer secret.
		//
		// UpdateColumns, not Updates, and that is the whole reason this line
		// looks unusual: Updates asks GORM to maintain the model's UpdatedAt
		// field, so it silently appends `updated_at = now()` to the statement.
		// UpdateColumns writes exactly the columns named.
		//
		// updated_at must NOT move. identityruntime keys its provider cache on
		// `id@updated_at` (see resolver.cacheKey), so bumping it would evict
		// every cached provider and make each affected workspace fetch a fresh
		// service-account token from Keycloak — churn caused by re-encrypting a
		// secret that did not change. The Connection's configuration and its
		// credential are both exactly what they were; only the wrapping moved,
		// and the cached provider holds the plaintext, which is still correct.
		//
		// This is the distinction the whole slice turns on: master-key rotation
		// is not credential rotation. Changing a client secret DOES bump
		// updated_at, through Repository.UpdateConfig, and must — that cached
		// provider holds a secret that no longer works.
		res := tx.Model(&row{}).
			Where("id = ? AND secret_key_version = ?", id, sealed.SecretKeyVersion).
			UpdateColumns(map[string]any{
				"secret_ciphertext":  reSealed.Ciphertext,
				"secret_nonce":       reSealed.Nonce,
				"secret_key_version": reSealed.KeyVersion,
				"secret_alg":         reSealed.Algorithm,
			})
		if res.Error != nil {
			outcome, failure = fail(sealed.SecretKeyVersion, "write_failed")
			return res.Error
		}
		if res.RowsAffected != 1 {
			outcome, failure = fail(sealed.SecretKeyVersion, "write_lost")
			return errors.New("connection: rotation write matched no row")
		}

		outcome = OutcomeRotated
		return nil
	})
	if err != nil && outcome != OutcomeFailed {
		// The transaction itself failed — commit error, cancelled context —
		// after the closure had decided things went well. The row did not move.
		outcome, failure = fail(observed, "write_failed")
	}
	return outcome, failure
}

// wipeSecret zeroes a buffer that held plaintext credential material.
//
// Mirrors identityruntime.wipe. It does not make the secret unrecoverable —
// Seal copies it — but it keeps the decrypted value from sitting in a heap
// buffer for the rest of the run, and the run touches every credential in the
// installation at once.
//
// A package variable rather than a plain function, and that is worth
// justifying, because a mutable function value in production code is normally a
// smell. The reason is that this is the one security property in the rotation
// path with NO observable effect: a rotation that never wipes produces byte-for-
// byte identical rows, an identical report and an identical exit code. Deleting
// the call would be invisible to every other test in this package.
//
// Making it swappable lets TestRotate_WipesThePlaintextBuffer see the buffer the
// rotation actually handed it, confirm it held the decrypted credential, and
// confirm it is zero afterwards. Unexported and never reassigned outside a
// test, so nothing reachable from a running process can change it.
var wipeSecret = func(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
