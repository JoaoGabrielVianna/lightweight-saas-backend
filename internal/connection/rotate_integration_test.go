//go:build integration

package connection

import (
	"bytes"
	"context"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"gorm.io/gorm"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// Master-key rotation, against a real PostgreSQL.
//
// Everything here needs the database rather than a fake, and for a specific
// reason each time: the row lock that makes concurrent rotation safe, the
// per-row transaction that makes a partial failure resumable, and the fact that
// the ciphertext on disk actually changed. None of those are expressible
// against an in-memory repository — a fake would prove the rotator calls the
// methods it calls.

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// twoKeyRing builds a keyring holding v1 and v2 with `current` as the
// encryption version. Deterministic material so a failure is reproducible.
func twoKeyRing(t *testing.T, current int) *secrets.Keyring {
	t.Helper()
	ring, err := secrets.NewKeyring([]secrets.KeyMaterial{
		{Version: 1, Key: bytes.Repeat([]byte{0xA1}, secrets.KeySize)},
		{Version: 2, Key: bytes.Repeat([]byte{0xB2}, secrets.KeySize)},
	}, current)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	return ring
}

// oneKeyRing builds a keyring holding only `version`, with the same material
// twoKeyRing uses for it. This is how "the operator removed the old key" and
// "the operator has not added the new one yet" are both expressed.
func oneKeyRing(t *testing.T, version int) *secrets.Keyring {
	t.Helper()
	fill := map[int]byte{1: 0xA1, 2: 0xB2}[version]
	ring, err := secrets.NewSingleVersionKeyring(version, bytes.Repeat([]byte{fill}, secrets.KeySize))
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	return ring
}

// sealedOf reads a row's secret columns straight from the table, bypassing the
// repository. Tests assert on what is ON DISK, not on what a method reports.
func sealedOf(t *testing.T, db *gorm.DB, id string) secrets.Sealed {
	t.Helper()
	var out struct {
		SecretCiphertext []byte
		SecretNonce      []byte
		SecretKeyVersion int
		SecretAlg        string
	}
	err := db.Model(&row{}).
		Select("secret_ciphertext", "secret_nonce", "secret_key_version", "secret_alg").
		Where("id = ?", id).Take(&out).Error
	if err != nil {
		t.Fatalf("read sealed columns for %s: %v", id, err)
	}
	return secrets.Sealed{
		Ciphertext: out.SecretCiphertext,
		Nonce:      out.SecretNonce,
		KeyVersion: out.SecretKeyVersion,
		Algorithm:  out.SecretAlg,
	}
}

func updatedAtOf(t *testing.T, db *gorm.DB, id string) time.Time {
	t.Helper()
	var out struct{ UpdatedAt time.Time }
	if err := db.Model(&row{}).Select("updated_at").Where("id = ?", id).Take(&out).Error; err != nil {
		t.Fatalf("read updated_at for %s: %v", id, err)
	}
	return out.UpdatedAt
}

// insertSealedUnder writes a connection whose secret is sealed with a specific
// keyring, so a test can construct a mixed-version table directly.
func insertSealedUnder(t *testing.T, repo *PostgresRepository, ring *secrets.Keyring, workspaceID, name, plaintext string) string {
	t.Helper()

	id, err := publicid.New()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	sealed, err := ring.Seal([]byte(plaintext), secretAAD(id))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	c := &Connection{
		ID: id, WorkspaceID: workspaceID, Name: name,
		Provider: ProviderKeycloak, Status: StatusDraft,
		BaseURL: "https://kc.example.com", Realm: "saas", ClientID: "svc",
		Health: HealthUnknown, AccessMode: AccessModeUnknown,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(context.Background(), c, sealed); err != nil {
		t.Fatalf("create connection %q: %v", name, err)
	}
	return id
}

// ---------------------------------------------------------------------------
// The rotation itself
// ---------------------------------------------------------------------------

// TestRotate_ReSealsUnderTheCurrentKey is acceptance criteria 18 and 19 in one:
// after rotation the row names the new version and holds different ciphertext,
// and the PLAINTEXT is byte-for-byte what it was.
//
// That last part is the whole point. A rotation that changed the provider
// secret would be a rotation that broke every workspace it touched.
func TestRotate_ReSealsUnderTheCurrentKey(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	v1 := oneKeyRing(t, 1)
	const plaintext = "provider-client-secret-value"
	id := insertSealedUnder(t, repo, v1, workspaceID, "Prod", plaintext)

	before := sealedOf(t, db, id)
	if before.KeyVersion != 1 {
		t.Fatalf("fixture is sealed under v%d, want v1", before.KeyVersion)
	}
	updatedBefore := updatedAtOf(t, db, id)

	rotator := NewRotator(db, twoKeyRing(t, 2))
	report, err := rotator.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Rotated != 1 || report.Failed() != 0 || report.Total != 1 {
		t.Fatalf("report = %+v, want 1 rotated of 1 total with no failures", report)
	}

	after := sealedOf(t, db, id)
	if after.KeyVersion != 2 {
		t.Errorf("secret_key_version = %d after rotation, want 2", after.KeyVersion)
	}
	if bytes.Equal(after.Ciphertext, before.Ciphertext) {
		t.Error("ciphertext is unchanged — the row was re-stamped without being re-encrypted")
	}
	if bytes.Equal(after.Nonce, before.Nonce) {
		t.Error("nonce is unchanged — a reused nonce under a new key is the one thing GCM cannot survive")
	}
	if after.Algorithm != secrets.AlgorithmAESGCM {
		t.Errorf("algorithm = %q after rotation", after.Algorithm)
	}

	// The provider secret must be identical. Opened with v2, since that is what
	// the row now says.
	opened, err := twoKeyRing(t, 2).Open(after, secretAAD(id))
	if err != nil {
		t.Fatalf("open the rotated row: %v", err)
	}
	if string(opened) != plaintext {
		t.Errorf("plaintext after rotation = %q, want %q — rotation changed the credential", opened, plaintext)
	}

	// The old key must no longer be needed by this row.
	if _, err := oneKeyRing(t, 2).Open(after, secretAAD(id)); err != nil {
		t.Errorf("a v2-only keyring cannot open the rotated row: %v", err)
	}

	// updated_at is untouched: identityruntime keys its provider cache on it,
	// and a master-key rotation changes no configuration and no credential.
	if got := updatedAtOf(t, db, id); !got.Equal(updatedBefore) {
		t.Errorf("updated_at moved from %s to %s — that evicts every cached provider "+
			"and makes each workspace re-authenticate against Keycloak for a secret that did not change",
			updatedBefore, got)
	}
}

// TestRotate_IsIdempotent — acceptance criteria 12 and 13. A second run must
// report zero work and, crucially, must not re-encrypt: a fresh nonce for an
// unchanged secret would make every retry look like something happened.
func TestRotate_IsIdempotent(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	id := insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Prod", "s3cr3t")
	rotator := NewRotator(db, twoKeyRing(t, 2))

	if _, err := rotator.Rotate(context.Background()); err != nil {
		t.Fatalf("first Rotate: %v", err)
	}
	afterFirst := sealedOf(t, db, id)

	second, err := rotator.Rotate(context.Background())
	if err != nil {
		t.Fatalf("second Rotate: %v", err)
	}
	if second.Rotated != 0 {
		t.Errorf("second run rotated %d rows, want 0", second.Rotated)
	}
	if second.AlreadyCurrent != 1 {
		t.Errorf("second run reported %d already current, want 1", second.AlreadyCurrent)
	}

	afterSecond := sealedOf(t, db, id)
	if !bytes.Equal(afterFirst.Ciphertext, afterSecond.Ciphertext) {
		t.Error("an already-current row was re-encrypted on the second run")
	}
	if !bytes.Equal(afterFirst.Nonce, afterSecond.Nonce) {
		t.Error("an already-current row got a new nonce on the second run")
	}
}

// TestRotate_MixedVersions is the brief's mixed-version case: A and B on v1, C
// already on v2, with v2 current. All three end on v2 and C is classified
// already_current rather than re-encrypted.
func TestRotate_MixedVersions(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	v1 := oneKeyRing(t, 1)
	v2Current := twoKeyRing(t, 2)

	idA := insertSealedUnder(t, repo, v1, workspaceID, "A", "secret-a")
	idB := insertSealedUnder(t, repo, v1, workspaceID, "B", "secret-b")
	idC := insertSealedUnder(t, repo, v2Current, workspaceID, "C", "secret-c")

	cBefore := sealedOf(t, db, idC)

	report, err := NewRotator(db, v2Current).Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Total != 3 {
		t.Errorf("total = %d, want 3", report.Total)
	}
	if report.Rotated != 2 {
		t.Errorf("rotated = %d, want 2 (A and B)", report.Rotated)
	}
	if report.AlreadyCurrent != 1 {
		t.Errorf("already_current = %d, want 1 (C)", report.AlreadyCurrent)
	}
	if report.Failed() != 0 {
		t.Errorf("failures = %v, want none", report.Failures)
	}

	for name, id := range map[string]string{"A": idA, "B": idB, "C": idC} {
		if got := sealedOf(t, db, id).KeyVersion; got != 2 {
			t.Errorf("connection %s is on v%d, want v2", name, got)
		}
	}

	cAfter := sealedOf(t, db, idC)
	if !bytes.Equal(cBefore.Ciphertext, cAfter.Ciphertext) {
		t.Error("C was already current and was re-encrypted anyway")
	}

	// Plaintexts survived.
	for name, pair := range map[string][2]string{
		"A": {idA, "secret-a"}, "B": {idB, "secret-b"}, "C": {idC, "secret-c"},
	} {
		opened, err := v2Current.Open(sealedOf(t, db, pair[0]), secretAAD(pair[0]))
		if err != nil {
			t.Errorf("open %s: %v", name, err)
			continue
		}
		if string(opened) != pair[1] {
			t.Errorf("%s opened to %q, want %q", name, opened, pair[1])
		}
	}
}

// TestRotate_RotatesEveryStatus. Draft, active and retired rows all hold a
// sealed credential, so all three must rotate — a retired row left on v1 makes
// the old key impossible to retire, forever, for a connection nobody uses.
func TestRotate_RotatesEveryStatus(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)
	ctx := context.Background()

	v1 := oneKeyRing(t, 1)
	draft := insertSealedUnder(t, repo, v1, workspaceID, "Draft", "s-draft")
	active := insertSealedUnder(t, repo, v1, workspaceID, "Active", "s-active")
	retired := insertSealedUnder(t, repo, v1, workspaceID, "Retired", "s-retired")

	markVerified(t, repo, active)
	if _, err := repo.Activate(ctx, active, workspaceID, time.Now().UTC()); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := repo.Retire(ctx, retired, time.Now().UTC()); err != nil {
		t.Fatalf("retire: %v", err)
	}

	report, err := NewRotator(db, twoKeyRing(t, 2)).Rotate(ctx)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Rotated != 3 {
		t.Errorf("rotated = %d, want 3 — every status holds a sealed credential", report.Rotated)
	}
	for name, id := range map[string]string{"draft": draft, "active": active, "retired": retired} {
		if got := sealedOf(t, db, id).KeyVersion; got != 2 {
			t.Errorf("%s connection is on v%d, want v2", name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Failure modes
// ---------------------------------------------------------------------------

// TestRotate_MissingKeyVersion — the brief's missing-key case. A row needs v1,
// the process holds only v2. The failure must be deterministic, diagnosable,
// must NOT silently try v2, and must leave the row exactly as it was.
func TestRotate_MissingKeyVersion(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	id := insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Orphan", "s3cr3t")
	before := sealedOf(t, db, id)

	report, err := NewRotator(db, oneKeyRing(t, 2)).Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate returned a run error for one bad row: %v", err)
	}
	if report.Rotated != 0 {
		t.Errorf("rotated = %d, want 0", report.Rotated)
	}
	if report.Failed() != 1 {
		t.Fatalf("failures = %v, want exactly one", report.Failures)
	}

	f := report.Failures[0]
	if f.Reason != "missing_key_version" {
		t.Errorf("reason = %q, want missing_key_version — an operator restores the key from this", f.Reason)
	}
	if f.KeyVersion != 1 {
		t.Errorf("failure names v%d, want v1", f.KeyVersion)
	}
	if f.ConnectionPublicID != publicid.Format(publicid.ConnectionPrefix, id) {
		t.Errorf("failure names %q, want the connection's public id", f.ConnectionPublicID)
	}

	// The row is untouched, which is what makes the failure recoverable.
	after := sealedOf(t, db, id)
	if after.KeyVersion != 1 || !bytes.Equal(after.Ciphertext, before.Ciphertext) || !bytes.Equal(after.Nonce, before.Nonce) {
		t.Error("the row was mutated by a rotation that could not open it")
	}

	// And it is still readable once the key comes back — the definitive proof
	// that nothing was lost.
	opened, err := twoKeyRing(t, 2).Open(after, secretAAD(id))
	if err != nil {
		t.Fatalf("row unreadable after a failed rotation: %v", err)
	}
	if string(opened) != "s3cr3t" {
		t.Errorf("plaintext = %q after a failed rotation", opened)
	}
}

// TestRotate_WrongKeyMaterial — the brief's wrong-key case, deliberately
// distinct from the missing-key one. Version 1 IS configured; the bytes behind
// it are not the bytes the row was sealed with. AES-GCM authentication must
// fail, no other key may be tried, and the row must not move.
func TestRotate_WrongKeyMaterial(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	id := insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Prod", "s3cr3t")
	before := sealedOf(t, db, id)

	// v1 present, wrong material. v2 holds the RIGHT bytes under the wrong
	// number, so a try-all-keys implementation would succeed here.
	wrong, err := secrets.NewKeyring([]secrets.KeyMaterial{
		{Version: 1, Key: bytes.Repeat([]byte{0xFF}, secrets.KeySize)},
		{Version: 2, Key: bytes.Repeat([]byte{0xA1}, secrets.KeySize)},
	}, 2)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}

	report, err := NewRotator(db, wrong).Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Failed() != 1 {
		t.Fatalf("failures = %v, want exactly one", report.Failures)
	}
	if got := report.Failures[0].Reason; got != "cannot_open" {
		t.Errorf("reason = %q, want cannot_open — this is not a missing key, and the fixes differ", got)
	}
	if report.Rotated != 0 {
		t.Error("a row opened with the wrong key material was rotated — a fallback key was tried")
	}

	after := sealedOf(t, db, id)
	if after.KeyVersion != 1 || !bytes.Equal(after.Ciphertext, before.Ciphertext) {
		t.Error("the row was mutated by a rotation whose key did not authenticate")
	}
}

// TestRotate_OneBadRowDoesNotBlockTheOthers is the partial-failure model,
// stated as a property: model B, per-row transactions, resumable.
func TestRotate_OneBadRowDoesNotBlockTheOthers(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)
	ctx := context.Background()

	v1 := oneKeyRing(t, 1)
	good1 := insertSealedUnder(t, repo, v1, workspaceID, "Good1", "s-1")
	good2 := insertSealedUnder(t, repo, v1, workspaceID, "Good2", "s-2")

	// A row sealed under a version nobody will configure during the first run.
	v3, err := secrets.NewSingleVersionKeyring(3, bytes.Repeat([]byte{0xC3}, secrets.KeySize))
	if err != nil {
		t.Fatalf("build v3 keyring: %v", err)
	}
	orphan := insertSealedUnder(t, repo, v3, workspaceID, "Orphan", "s-3")

	report, err := NewRotator(db, twoKeyRing(t, 2)).Rotate(ctx)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Rotated != 2 {
		t.Errorf("rotated = %d, want 2 — one undecryptable row must not discard the others' work", report.Rotated)
	}
	if report.Failed() != 1 {
		t.Errorf("failures = %v, want exactly the orphan", report.Failures)
	}
	for _, id := range []string{good1, good2} {
		if got := sealedOf(t, db, id).KeyVersion; got != 2 {
			t.Errorf("a healthy row is still on v%d", got)
		}
	}
	if got := sealedOf(t, db, orphan).KeyVersion; got != 3 {
		t.Errorf("the orphan row moved to v%d; it should be untouched", got)
	}

	// Restore the missing key and re-run: rotated rows are skipped, the
	// remaining one completes. This is the "rerun resumes" half of model B.
	withV3, err := secrets.NewKeyring([]secrets.KeyMaterial{
		{Version: 1, Key: bytes.Repeat([]byte{0xA1}, secrets.KeySize)},
		{Version: 2, Key: bytes.Repeat([]byte{0xB2}, secrets.KeySize)},
		{Version: 3, Key: bytes.Repeat([]byte{0xC3}, secrets.KeySize)},
	}, 2)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}

	resumed, err := NewRotator(db, withV3).Rotate(ctx)
	if err != nil {
		t.Fatalf("resumed Rotate: %v", err)
	}
	if resumed.Rotated != 1 {
		t.Errorf("resumed run rotated %d, want exactly the previously-failed row", resumed.Rotated)
	}
	if resumed.AlreadyCurrent != 2 {
		t.Errorf("resumed run reported %d already current, want 2", resumed.AlreadyCurrent)
	}
	if resumed.Failed() != 0 {
		t.Errorf("resumed run still reports failures: %v", resumed.Failures)
	}
	opened, err := withV3.Open(sealedOf(t, db, orphan), secretAAD(orphan))
	if err != nil || string(opened) != "s-3" {
		t.Errorf("the resumed row does not hold its original secret: %q %v", opened, err)
	}
}

// TestRotate_InterruptedRunResumes. A cancelled context stops the run between
// rows — never inside one — and the rows that committed stay committed.
func TestRotate_InterruptedRunResumes(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	v1 := oneKeyRing(t, 1)
	const total = 8
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		ids = append(ids, insertSealedUnder(t, repo, v1, workspaceID,
			"C"+string(rune('A'+i)), "secret-"+string(rune('A'+i))))
	}

	// Cancel shortly after the run starts. Where exactly it lands is not
	// asserted — the property is that whatever it did is consistent and
	// re-runnable, not that it stopped at a particular row.
	ctx, cancel := context.WithCancel(context.Background())
	rotator := NewRotator(db, twoKeyRing(t, 2))

	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()
	partial, err := rotator.Rotate(ctx)
	if err == nil && partial.Rotated != total {
		t.Fatalf("run neither finished nor reported an interruption: %+v", partial)
	}

	// Every row is either fully v1 or fully v2 — never a torn mixture of new
	// ciphertext under an old version stamp, which is the corruption a per-row
	// transaction exists to prevent.
	v1Only, v2Ring := oneKeyRing(t, 1), twoKeyRing(t, 2)
	for i, id := range ids {
		want := "secret-" + string(rune('A'+i))
		sealed := sealedOf(t, db, id)
		var opened []byte
		var openErr error
		switch sealed.KeyVersion {
		case 1:
			opened, openErr = v1Only.Open(sealed, secretAAD(id))
		case 2:
			opened, openErr = v2Ring.Open(sealed, secretAAD(id))
		default:
			t.Fatalf("row %s is on unexpected version %d", id, sealed.KeyVersion)
		}
		if openErr != nil {
			t.Fatalf("row %s (v%d) does not open after an interrupted run: %v",
				id, sealed.KeyVersion, openErr)
		}
		if string(opened) != want {
			t.Errorf("row %s holds %q, want %q", id, opened, want)
		}
	}

	// Re-run to completion. This is the documented recovery.
	final, err := rotator.Rotate(context.Background())
	if err != nil {
		t.Fatalf("resumed Rotate: %v", err)
	}
	if final.Failed() != 0 {
		t.Fatalf("resumed run failed: %v", final.Failures)
	}
	if final.AlreadyCurrent+final.Rotated != total {
		t.Errorf("resumed run accounted for %d rows, want %d", final.AlreadyCurrent+final.Rotated, total)
	}
	for i, id := range ids {
		sealed := sealedOf(t, db, id)
		if sealed.KeyVersion != 2 {
			t.Errorf("row %s is still on v%d after the resumed run", id, sealed.KeyVersion)
		}
		opened, err := v2Ring.Open(sealed, secretAAD(id))
		if err != nil || string(opened) != "secret-"+string(rune('A'+i)) {
			t.Errorf("row %s lost its secret: %q %v", id, opened, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// TestRotate_ConcurrentRotationsCannotCorruptARow.
//
// Four rotators over the same rows at once, which is the shape of an operator
// running the command twice or of a second process existing later. The
// guarantee comes from SELECT ... FOR UPDATE in the database rather than from a
// mutex in this process, because a mutex would be worth nothing to the second
// process.
func TestRotate_ConcurrentRotationsCannotCorruptARow(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	v1 := oneKeyRing(t, 1)
	const total = 12
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		ids = append(ids, insertSealedUnder(t, repo, v1, workspaceID,
			"C"+string(rune('A'+i)), "secret-"+string(rune('A'+i))))
	}

	ring := twoKeyRing(t, 2)
	var wg sync.WaitGroup
	problems := make(chan string, 16)
	rotatedTotal := make(chan int, 4)

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report, err := NewRotator(db, ring).Rotate(context.Background())
			if err != nil {
				problems <- "rotate: " + err.Error()
				return
			}
			if report.Failed() != 0 {
				problems <- "failures under concurrency: " + report.Failures[0].String()
			}
			rotatedTotal <- report.Rotated
		}()
	}
	wg.Wait()
	close(problems)
	close(rotatedTotal)

	for msg := range problems {
		t.Error(msg)
	}

	// Each row is rotated exactly once across all four runners. More would mean
	// a row was re-encrypted after already being current — a lost lock.
	sum := 0
	for n := range rotatedTotal {
		sum += n
	}
	if sum != total {
		t.Errorf("the four rotators rotated %d rows between them, want exactly %d — "+
			"a higher number means a row was rotated twice", sum, total)
	}

	// Nothing was lost or mangled.
	for i, id := range ids {
		sealed := sealedOf(t, db, id)
		if sealed.KeyVersion != 2 {
			t.Errorf("row %s is on v%d after concurrent rotation", id, sealed.KeyVersion)
		}
		opened, err := ring.Open(sealed, secretAAD(id))
		if err != nil {
			t.Errorf("row %s does not open after concurrent rotation: %v", id, err)
			continue
		}
		if want := "secret-" + string(rune('A'+i)); string(opened) != want {
			t.Errorf("row %s holds %q, want %q", id, opened, want)
		}
	}
}

// TestRotate_ConnectionCreatedDuringRotationUsesTheCurrentKey — acceptance
// criterion 25. A row created while v2 is current must be on v2 immediately; it
// must not inherit v1 because a rotation happens to be running.
func TestRotate_ConnectionCreatedDuringRotationUsesTheCurrentKey(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)
	ctx := context.Background()

	v1 := oneKeyRing(t, 1)
	for i := 0; i < 6; i++ {
		insertSealedUnder(t, repo, v1, workspaceID, "Old"+string(rune('A'+i)), "old-secret")
	}

	ring := twoKeyRing(t, 2)
	svc := NewService(repo, workspace.NewRepository(db), ring, &fakeVerifier{report: okReport()}, database.NewTxRunner(db), &fakeAuditWriter{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := NewRotator(db, ring).Rotate(ctx); err != nil {
			t.Errorf("Rotate: %v", err)
		}
	}()

	created, err := svc.Create(ctx, publicid.Format(publicid.WorkspacePrefix, workspaceID), CreateInput{
		Name: "Created mid-rotation", BaseURL: "https://kc.example.com",
		Realm: "saas", ClientID: "svc", ClientSecret: "brand-new-secret",
	}, testEvent(audit.ActionConnectionCreated))
	if err != nil {
		t.Fatalf("create during rotation: %v", err)
	}
	wg.Wait()

	if got := sealedOf(t, db, created.ID).KeyVersion; got != 2 {
		t.Errorf("a connection created while v2 was current is sealed under v%d", got)
	}

	// And a full rotation afterwards leaves it alone.
	report, err := NewRotator(db, ring).Rotate(ctx)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Rotated != 0 {
		t.Errorf("a follow-up rotation rotated %d rows; everything should already be current", report.Rotated)
	}
}

// TestRotate_DoesNotResurrectAReplacedCredential is the interleaving that would
// actually destroy data: read the old ciphertext, let a new client secret land,
// then write the OLD plaintext back re-sealed. The row lock is what prevents
// it; this proves the outcome rather than the mechanism.
func TestRotate_DoesNotResurrectAReplacedCredential(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)
	ctx := context.Background()

	ring := twoKeyRing(t, 2)
	svc := NewService(repo, workspace.NewRepository(db), ring, &fakeVerifier{report: okReport()}, database.NewTxRunner(db), &fakeAuditWriter{})

	id := insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Prod", "revoked-secret")
	wsPublic := publicid.Format(publicid.WorkspacePrefix, workspaceID)
	connPublic := publicid.Format(publicid.ConnectionPrefix, id)

	replacement := "the-replacement-secret"
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := NewRotator(db, ring).Rotate(ctx); err != nil {
			t.Errorf("Rotate: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := svc.Update(ctx, wsPublic, connPublic, UpdateInput{ClientSecret: &replacement}, testEvent(audit.ActionConnectionUpdated)); err != nil {
			t.Errorf("Update: %v", err)
		}
	}()
	wg.Wait()

	sealed := sealedOf(t, db, id)
	opened, err := ring.Open(sealed, secretAAD(id))
	if err != nil {
		t.Fatalf("open after the race: %v", err)
	}
	if string(opened) == "revoked-secret" {
		t.Error("rotation wrote the OLD plaintext back over a credential that had just been replaced")
	}
	if string(opened) != replacement {
		t.Errorf("connection holds %q, want the replacement secret", opened)
	}
}

// ---------------------------------------------------------------------------
// Census and key removal
// ---------------------------------------------------------------------------

// TestCensus_AnswersWhenAnOldKeyCanBeDestroyed — acceptance criteria 26 and 27.
func TestCensus_AnswersWhenAnOldKeyCanBeDestroyed(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)
	ctx := context.Background()

	v1 := oneKeyRing(t, 1)
	insertSealedUnder(t, repo, v1, workspaceID, "A", "s-a")
	insertSealedUnder(t, repo, v1, workspaceID, "B", "s-b")

	ring := twoKeyRing(t, 2)
	rotator := NewRotator(db, ring)

	before, err := rotator.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	if before.Rows[1] != 2 || before.Total() != 2 {
		t.Fatalf("census before = %+v, want two rows on v1", before.Rows)
	}
	if safe := before.SafeToRemove(ring); len(safe) != 0 {
		t.Errorf("v%v reported safe to remove while %d rows still need it", safe, before.Rows[1])
	}
	if missing := before.Unopenable(ring); len(missing) != 0 {
		t.Errorf("Unopenable = %v with both keys configured", missing)
	}

	if _, err := rotator.Rotate(ctx); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	after, err := rotator.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	if after.Rows[1] != 0 {
		t.Errorf("v1 still has %d rows after a complete rotation", after.Rows[1])
	}
	if after.Rows[2] != 2 {
		t.Errorf("v2 has %d rows, want 2", after.Rows[2])
	}

	safe := after.SafeToRemove(ring)
	if len(safe) != 1 || safe[0] != 1 {
		t.Errorf("SafeToRemove = %v, want [1] once nothing needs v1", safe)
	}
	// The current key is never proposed for removal, however empty the table.
	for _, v := range safe {
		if v == ring.CurrentVersion() {
			t.Error("the CURRENT key was reported safe to remove")
		}
	}
}

// TestCensus_ReportsAVersionTheProcessCannotOpen is the startup-validation
// signal: it must be answerable WITHOUT decrypting anything, which is what
// makes it cheap enough to run on a ticker.
func TestCensus_ReportsAVersionTheProcessCannotOpen(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Orphan", "s3cr3t")

	onlyV2 := oneKeyRing(t, 2)
	census, err := NewRotator(db, onlyV2).Census(context.Background())
	if err != nil {
		t.Fatalf("Census: %v", err)
	}

	missing := census.Unopenable(onlyV2)
	if len(missing) != 1 || missing[0] != 1 {
		t.Errorf("Unopenable = %v, want [1]", missing)
	}
}

// TestPlan_DryRunTouchesNothing. A dry run must report and not write, and must
// not claim more than metadata can support.
func TestPlan_DryRunTouchesNothing(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	v1 := oneKeyRing(t, 1)
	idA := insertSealedUnder(t, repo, v1, workspaceID, "A", "s-a")
	ring := twoKeyRing(t, 2)
	idB := insertSealedUnder(t, repo, ring, workspaceID, "B", "s-b")

	beforeA := sealedOf(t, db, idA)
	beforeB := sealedOf(t, db, idB)

	plan, err := NewRotator(db, ring).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Current != 2 {
		t.Errorf("plan.Current = %d, want 2", plan.Current)
	}
	if plan.NeedRotation != 1 {
		t.Errorf("NeedRotation = %d, want 1", plan.NeedRotation)
	}
	if plan.AlreadyCurrent != 1 {
		t.Errorf("AlreadyCurrent = %d, want 1", plan.AlreadyCurrent)
	}
	if plan.Blocked != 0 {
		t.Errorf("Blocked = %d, want 0 with every version configured", plan.Blocked)
	}

	if afterA := sealedOf(t, db, idA); !bytes.Equal(afterA.Ciphertext, beforeA.Ciphertext) || afterA.KeyVersion != beforeA.KeyVersion {
		t.Error("a dry run modified a row")
	}
	if afterB := sealedOf(t, db, idB); !bytes.Equal(afterB.Ciphertext, beforeB.Ciphertext) {
		t.Error("a dry run modified an already-current row")
	}
}

// TestPlan_ReportsBlockedRows — a dry run CAN detect a missing key version,
// because the version is metadata.
func TestPlan_ReportsBlockedRows(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Orphan", "s3cr3t")

	plan, err := NewRotator(db, oneKeyRing(t, 2)).Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Blocked != 1 {
		t.Errorf("Blocked = %d, want 1", plan.Blocked)
	}
	if len(plan.MissingVersions) != 1 || plan.MissingVersions[0] != 1 {
		t.Errorf("MissingVersions = %v, want [1]", plan.MissingVersions)
	}
}

// ---------------------------------------------------------------------------
// Secret isolation
// ---------------------------------------------------------------------------

// TestRotate_NeverExposesSecretMaterial. Unique sentinels through the whole
// rotation surface: the report, the failure lines, the CLI output. None of them
// may carry a plaintext, a key, or a ciphertext.
func TestRotate_NeverExposesSecretMaterial(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	const plaintextSentinel = "SENTINEL-PLAINTEXT-4a91c07e"
	v1 := oneKeyRing(t, 1)
	id := insertSealedUnder(t, repo, v1, workspaceID, "Prod", plaintextSentinel)

	// A second row that cannot be opened, so failure output is exercised too.
	v3, err := secrets.NewSingleVersionKeyring(3, bytes.Repeat([]byte{0xC3}, secrets.KeySize))
	if err != nil {
		t.Fatalf("build v3 keyring: %v", err)
	}
	insertSealedUnder(t, repo, v3, workspaceID, "Orphan", "SENTINEL-ORPHAN-9f22b1de")

	ring := twoKeyRing(t, 2)
	var out, errOut bytes.Buffer
	code := RunSecretsCLI(context.Background(), []string{"rotate"}, SecretsCLIDeps{
		DB: db, Keyring: ring, Configured: true,
	}, &out, &errOut)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 with one unrotatable row", code)
	}

	var statusOut, statusErr bytes.Buffer
	if RunSecretsCLI(context.Background(), []string{"status"}, SecretsCLIDeps{
		DB: db, Keyring: ring, Configured: true,
	}, &statusOut, &statusErr) != 1 {
		t.Error("status exit code should be 1 while a persisted version is unconfigured")
	}

	sealed := sealedOf(t, db, id)
	forbidden := map[string]string{
		"the plaintext secret":  plaintextSentinel,
		"the orphan's secret":   "SENTINEL-ORPHAN-9f22b1de",
		"base64 master key v1":  secrets.EncodeKey(bytes.Repeat([]byte{0xA1}, secrets.KeySize)),
		"base64 master key v2":  secrets.EncodeKey(bytes.Repeat([]byte{0xB2}, secrets.KeySize)),
		"the stored ciphertext": string(sealed.Ciphertext),
	}

	surfaces := map[string]string{
		"rotate stdout": out.String(),
		"rotate stderr": errOut.String(),
		"status stdout": statusOut.String(),
		"status stderr": statusErr.String(),
	}
	for surface, text := range surfaces {
		for label, secret := range forbidden {
			if secret != "" && strings.Contains(text, secret) {
				t.Errorf("%s contains %s", surface, label)
			}
		}
	}

	// The database must hold no plaintext either — the property the whole
	// scheme exists for, asserted against the bytes actually stored.
	if bytes.Contains(sealed.Ciphertext, []byte(plaintextSentinel)) {
		t.Error("the stored ciphertext contains the plaintext")
	}
	var rawRows []struct{ Blob []byte }
	if err := db.Raw(`SELECT secret_ciphertext AS blob FROM connections`).Scan(&rawRows).Error; err != nil {
		t.Fatalf("read ciphertexts: %v", err)
	}
	for _, r := range rawRows {
		if bytes.Contains(r.Blob, []byte("SENTINEL")) {
			t.Error("a stored ciphertext contains a plaintext sentinel")
		}
	}
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

// TestSecretsCLI_ExitCodes — the contract a deploy script branches on.
func TestSecretsCLI_ExitCodes(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	ring := twoKeyRing(t, 2)
	deps := SecretsCLIDeps{DB: db, Keyring: ring, Configured: true}
	run := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := RunSecretsCLI(context.Background(), args, deps, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	t.Run("no arguments is a usage error", func(t *testing.T) {
		if code, _, _ := run(); code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
	})

	t.Run("unknown command is a usage error", func(t *testing.T) {
		if code, _, _ := run("rotato"); code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
	})

	t.Run("unknown flag is a usage error", func(t *testing.T) {
		if code, _, _ := run("rotate", "--force"); code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
	})

	t.Run("--dry-run on status is a usage error", func(t *testing.T) {
		if code, _, _ := run("status", "--dry-run"); code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
	})

	t.Run("no keyring configured is a configuration error", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := RunSecretsCLI(context.Background(), []string{"status"},
			SecretsCLIDeps{DB: db}, &out, &errOut)
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
	})

	t.Run("an empty installation succeeds", func(t *testing.T) {
		code, out, _ := run("rotate")
		if code != 0 {
			t.Errorf("exit code = %d, want 0 with nothing to do", code)
		}
		if !strings.Contains(out, "rotated:") {
			t.Errorf("output does not report what happened:\n%s", out)
		}
	})

	t.Run("a successful rotation exits 0", func(t *testing.T) {
		insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Prod", "s3cr3t")
		if code, _, _ := run("rotate"); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		// And again, idempotently.
		if code, _, _ := run("rotate"); code != 0 {
			t.Errorf("second run exit code = %d, want 0", code)
		}
		if code, out, _ := run("status"); code != 0 {
			t.Errorf("status exit code = %d, want 0\n%s", code, out)
		}
	})

	t.Run("status names the key that became safe to remove", func(t *testing.T) {
		code, out, _ := run("status")
		if code != 0 {
			t.Fatalf("status exit code = %d", code)
		}
		if !strings.Contains(out, "Safe to remove:") || !strings.Contains(out, "v1") {
			t.Errorf("status does not report v1 as removable:\n%s", out)
		}
	})

	t.Run("dry run reports without writing", func(t *testing.T) {
		id := insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Fresh", "s3cr3t")
		before := sealedOf(t, db, id)

		code, out, _ := run("rotate", "--dry-run")
		if code != 0 {
			t.Errorf("dry-run exit code = %d, want 0", code)
		}
		if !strings.Contains(out, "Would rotate:") {
			t.Errorf("dry run does not say what it would do:\n%s", out)
		}
		if after := sealedOf(t, db, id); !bytes.Equal(after.Ciphertext, before.Ciphertext) {
			t.Error("a dry run wrote to the database")
		}
	})
}

// ---------------------------------------------------------------------------
// Seeding tool
// ---------------------------------------------------------------------------

// TestSeedConnectionsForRotationCheck writes connections for the shell harness
// in scripts/secrets-rotation-check.sh.
//
// A test that acts as a tool when asked, following the same pattern
// config.TestRenderContract_ToFile uses. The alternative — a small main under
// scripts/ — would be a package with no tests in a module whose coverage gate
// measures ./..., so a seeding utility would quietly cost the project a
// percentage point of its floor. This costs nothing and skips by default.
//
// Unlike every other test in this file it does NOT create a private schema: the
// harness passes a DSN already scoped to one, because the CLI it goes on to run
// has to see the same rows.
//
//	DB_URL=…?search_path=… SECRETS_MASTER_KEY=… \
//	LW_SEED_CONNECTIONS="secret-a,secret-b" \
//	go test -tags=integration -run TestSeedConnectionsForRotationCheck ./internal/connection/
func TestSeedConnectionsForRotationCheck(t *testing.T) {
	spec := os.Getenv("LW_SEED_CONNECTIONS")
	if spec == "" {
		t.Skip("LW_SEED_CONNECTIONS unset — this is a tool, not an assertion")
	}

	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Fatal("DB_URL unset")
	}
	db := openGorm(t, dsn)

	kc, err := config.LoadSecretsKeyring()
	if err != nil {
		t.Fatalf("read keyring configuration: %v", err)
	}
	if !kc.Configured() {
		t.Fatal("no keyring configured — nothing could be sealed")
	}
	ring, err := secrets.NewKeyringFromBase64(kc.Keys, kc.Current)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}

	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	for i, plaintext := range strings.Split(spec, ",") {
		plaintext = strings.TrimSpace(plaintext)
		if plaintext == "" {
			continue
		}
		id := insertSealedUnder(t, repo, ring, workspaceID, "Seeded"+strconv.Itoa(i), plaintext)
		// The public id, so the harness can correlate. Never the secret.
		t.Logf("seeded %s under key version %d",
			publicid.Format(publicid.ConnectionPrefix, id), ring.CurrentVersion())
	}
}

// TestRotate_WipesThePlaintextBuffer.
//
// The plaintext credential exists for the length of one transaction and is
// zeroed on the way out, so a panic dump or a core file taken later does not
// carry every provider secret in the installation in a live heap buffer.
//
// This is the one security property in the rotation path with no observable
// effect on the result: a rotation that never wiped would produce identical
// rows, an identical report and an identical exit code. Nothing else in this
// file would notice it being deleted, which is why the wipe is swappable and
// why this test is here rather than being left to a code comment.
func TestRotate_WipesThePlaintextBuffer(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	workspaceID := seedWorkspace(t, db)

	const plaintext = "SENTINEL-WIPE-ME-70b1f4c2"
	insertSealedUnder(t, repo, oneKeyRing(t, 1), workspaceID, "Prod", plaintext)

	real := wipeSecret
	t.Cleanup(func() { wipeSecret = real })

	var handed [][]byte
	sawPlaintext := false
	wipeSecret = func(b []byte) {
		if string(b) == plaintext {
			sawPlaintext = true
		}
		handed = append(handed, b)
		real(b)
	}

	report, err := NewRotator(db, twoKeyRing(t, 2)).Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Rotated != 1 {
		t.Fatalf("rotated = %d; the rotation did not run and this proves nothing", report.Rotated)
	}

	if len(handed) == 0 {
		t.Fatal("rotation never wiped anything — the decrypted credential was left in a heap buffer")
	}
	if !sawPlaintext {
		t.Error("the wipe was called, but never with the decrypted credential — " +
			"it is being applied to the wrong buffer")
	}
	for i, b := range handed {
		for j, c := range b {
			if c != 0 {
				t.Fatalf("buffer %d is not zeroed at byte %d", i, j)
			}
		}
	}
}
