//go:build integration

package identityruntime

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"gorm.io/gorm"
)

// Master-key rotation, end to end, against a real PostgreSQL and a real
// Keycloak.
//
// The rotation package's own integration tests prove the ROWS move correctly.
// This file proves the thing an operator actually cares about: that a workspace
// keeps working across the whole lifecycle, including after the old key has
// been destroyed. Nothing here is mocked — the credential being re-sealed is
// one Keycloak itself issued, and "still works" means Keycloak answered.

// ---------------------------------------------------------------------------
// Restartable fixture
// ---------------------------------------------------------------------------

// restartWith rebuilds the resolver and the connection service over a different
// keyring, against the SAME database.
//
// This is what a process restart with an edited SECRETS_KEYRING does, and it is
// modelled by rebuilding rather than by mutating because that is the real
// property under test: a NEW process, holding only the keys the environment now
// says, reading rows a previous process wrote. Mutating a keyring in place
// would leave the old keys reachable and prove nothing about removal.
func (i *installation) restartWith(ring *secrets.Keyring) {
	i.t.Helper()

	i.keyring = ring
	i.connSvc = connection.NewService(
		i.connections, i.workspaces, ring, connection.NewKeycloakVerifier(nil), database.NewTxRunner(i.db), noopAuditWriter{})
	i.resolver = NewResolver(i.workspaces, i.connections, ring, Options{})
	if i.resolver == nil {
		i.t.Fatal("NewResolver returned nil after restart")
	}
}

// rotationKeys is fixed material so a failure is reproducible. Test-only bytes;
// nothing here is or resembles a real key.
var rotationKeys = map[int][]byte{
	1: bytes.Repeat([]byte{0xA1}, secrets.KeySize),
	2: bytes.Repeat([]byte{0xB2}, secrets.KeySize),
}

func ringOf(t *testing.T, current int, versions ...int) *secrets.Keyring {
	t.Helper()
	materials := make([]secrets.KeyMaterial, 0, len(versions))
	for _, v := range versions {
		materials = append(materials, secrets.KeyMaterial{Version: v, Key: rotationKeys[v]})
	}
	ring, err := secrets.NewKeyring(materials, current)
	if err != nil {
		t.Fatalf("build keyring (current=%d, versions=%v): %v", current, versions, err)
	}
	return ring
}

// storedSecret reads a connection's sealed columns straight from the table.
func storedSecret(t *testing.T, db *gorm.DB, connectionID string) secrets.Sealed {
	t.Helper()
	var out struct {
		SecretCiphertext []byte
		SecretNonce      []byte
		SecretKeyVersion int
		SecretAlg        string
	}
	err := db.Table("connections").
		Select("secret_ciphertext", "secret_nonce", "secret_key_version", "secret_alg").
		Where("id = ?", connectionID).Take(&out).Error
	if err != nil {
		t.Fatalf("read stored secret: %v", err)
	}
	return secrets.Sealed{
		Ciphertext: out.SecretCiphertext,
		Nonce:      out.SecretNonce,
		KeyVersion: out.SecretKeyVersion,
		Algorithm:  out.SecretAlg,
	}
}

// mustListUsers resolves the workspace and lists its realm's users, failing the
// test if either step does not work. This is the "the runtime works" assertion,
// and it is a real round trip to Keycloak every time it is called.
func mustListUsers(t *testing.T, i *installation, ws *workspace.Workspace, stage string) []string {
	t.Helper()

	resolved, err := i.resolver.ForWorkspace(context.Background(), ws.PublicID())
	if err != nil {
		t.Fatalf("%s: resolve %s: %v", stage, ws.Slug, err)
	}
	users, err := resolved.Provider.ListUsers(context.Background(), identity.ListUsersQuery{Max: 100})
	if err != nil {
		t.Fatalf("%s: list users through %s: %v", stage, ws.Slug, err)
	}
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Username)
	}
	return names
}

// ---------------------------------------------------------------------------
// The acceptance test
// ---------------------------------------------------------------------------

// TestKeyRotation_ConnectionSurvivesTheWholeLifecycle is the slice's definition
// of success, in one test:
//
//	sealed under v1 → runtime works
//	  → add v2, keep v1, v2 current → runtime still works BEFORE rotating
//	  → rotate → the row says v2 and the ciphertext changed
//	  → restart with v2 ONLY, v1 destroyed → runtime still works
//
// Every "works" is a real admin call to a real Keycloak realm using the
// credential that was just re-wrapped. A test that only checked the columns
// would pass on a rotation that quietly corrupted the secret.
func TestKeyRotation_ConnectionSurvivesTheWholeLifecycle(t *testing.T) {
	inst := newInstallation(t)

	// ── Stage 1: an installation that has never rotated ──────────────────────
	inst.restartWith(ringOf(t, 1, 1))

	ws := inst.newWorkspace("rotating")
	conn := inst.connectRealm(ws, "rt-rotate-a", "primary")
	inst.kc.createUser("rt-rotate-a", "alice-before-rotation")

	if names := mustListUsers(t, inst, ws, "before rotation"); !contains(names, "alice-before-rotation") {
		t.Fatalf("the runtime does not work before rotation even starts: %v", names)
	}

	beforeRotation := storedSecret(t, inst.db, conn.ID)
	if beforeRotation.KeyVersion != 1 {
		t.Fatalf("connection is sealed under v%d, want v1", beforeRotation.KeyVersion)
	}

	// ── Stage 2: v2 added, v1 retained, v2 current ───────────────────────────
	//
	// The mixed state. Nothing has been rotated yet, so the row still needs v1
	// and the process must still be able to use it.
	inst.restartWith(ringOf(t, 2, 1, 2))

	if names := mustListUsers(t, inst, ws, "mixed-key state, before rotating"); !contains(names, "alice-before-rotation") {
		t.Errorf("the runtime broke merely by adding a second key: %v", names)
	}

	// A connection created NOW must use v2 immediately — it must not inherit
	// v1 just because the older row still does.
	fresh := inst.connectRealm(inst.newWorkspace("fresh"), "rt-rotate-b", "primary")
	if got := storedSecret(t, inst.db, fresh.ID).KeyVersion; got != 2 {
		t.Errorf("a connection created while v2 is current is sealed under v%d", got)
	}

	// ── Stage 3: rotate ──────────────────────────────────────────────────────
	rotator := connection.NewRotator(inst.db, inst.keyring)
	report, err := rotator.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if report.Failed() != 0 {
		t.Fatalf("rotation reported failures: %v", report.Failures)
	}
	if report.Rotated != 1 {
		t.Errorf("rotated = %d, want exactly the pre-existing v1 connection", report.Rotated)
	}
	if report.AlreadyCurrent != 1 {
		t.Errorf("already_current = %d, want exactly the connection created under v2", report.AlreadyCurrent)
	}

	afterRotation := storedSecret(t, inst.db, conn.ID)
	if afterRotation.KeyVersion != 2 {
		t.Errorf("secret_key_version = %d after rotation, want 2", afterRotation.KeyVersion)
	}
	if bytes.Equal(afterRotation.Ciphertext, beforeRotation.Ciphertext) {
		t.Error("the ciphertext did not change — the row was re-stamped, not re-encrypted")
	}

	// Still working while both keys are configured.
	if names := mustListUsers(t, inst, ws, "after rotation, both keys present"); !contains(names, "alice-before-rotation") {
		t.Errorf("the runtime broke immediately after rotation: %v", names)
	}

	// ── Stage 4: v1 destroyed ────────────────────────────────────────────────
	//
	// The proof. This process cannot open anything sealed under v1, and the
	// workspace still works — which is only possible if rotation genuinely
	// re-wrapped the credential rather than moving a version number.
	inst.restartWith(ringOf(t, 2, 2))

	inst.kc.createUser("rt-rotate-a", "bob-after-rotation")
	names := mustListUsers(t, inst, ws, "old key destroyed")
	if !contains(names, "alice-before-rotation") || !contains(names, "bob-after-rotation") {
		t.Errorf("the workspace stopped working after the old key was removed: %v", names)
	}

	// A write, not just a read: the re-sealed credential is still privileged
	// enough to mutate its realm.
	svc := identity.NewService(mustResolve(t, inst, ws).Provider)
	if _, err := svc.CreateRole(context.Background(), identity.CreateRoleRequest{
		Name: "post-rotation-role", Description: "written with a re-wrapped credential",
	}); err != nil {
		t.Fatalf("write through the rotated connection: %v", err)
	}
	if !contains(inst.kc.realmRoleNames("rt-rotate-a"), "post-rotation-role") {
		t.Error("the write did not land in the realm")
	}

	// And rotation is now a no-op.
	final, err := connection.NewRotator(inst.db, inst.keyring).Rotate(context.Background())
	if err != nil {
		t.Fatalf("final Rotate: %v", err)
	}
	if final.Rotated != 0 || final.Failed() != 0 {
		t.Errorf("a rotation after everything is current did work: %+v", final)
	}
}

// TestKeyRotation_RemovingAKeyStillNeededFailsOneWorkspaceOnly is the
// missing-key case at the runtime boundary, and it is the reason readiness does
// not check key coverage.
//
// One workspace's connection is left on v1 and the process is restarted holding
// only v2. That workspace answers credentials_unavailable; the other one, whose
// connection did rotate, keeps working. A readiness check on key coverage would
// have taken both of them out of service.
func TestKeyRotation_RemovingAKeyStillNeededFailsOneWorkspaceOnly(t *testing.T) {
	inst := newInstallation(t)
	inst.restartWith(ringOf(t, 1, 1))

	stranded := inst.newWorkspace("stranded")
	healthy := inst.newWorkspace("healthy")
	strandedConn := inst.connectRealm(stranded, "rt-strand-a", "primary")
	inst.connectRealm(healthy, "rt-strand-b", "primary")
	inst.kc.createUser("rt-strand-b", "still-here")

	// Move to v2 and rotate only the healthy workspace's row, by rotating with
	// the stranded one's version deliberately absent from the keyring.
	inst.restartWith(ringOf(t, 2, 1, 2))

	// Rotate everything, then push the stranded row back to v1 to model the
	// operator who removed the key before the rotation covered every row.
	if _, err := connection.NewRotator(inst.db, inst.keyring).Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	reSealUnderV1(t, inst, strandedConn.ID)

	// The old key is now gone from the process.
	inst.restartWith(ringOf(t, 2, 2))

	if _, err := inst.resolver.ForWorkspace(context.Background(), stranded.PublicID()); err == nil {
		t.Error("a workspace whose key was removed resolved successfully")
	} else if !strings.Contains(err.Error(), "credentials_unavailable") {
		t.Errorf("stranded workspace answered %q, want credentials_unavailable", err)
	}

	// The blast radius is one workspace.
	if names := mustListUsers(t, inst, healthy, "sibling of a stranded workspace"); !contains(names, "still-here") {
		t.Errorf("an unrelated workspace was taken down by another's missing key: %v", names)
	}

	// The census names the problem without opening anything.
	census, err := connection.NewRotator(inst.db, inst.keyring).Census(context.Background())
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	missing := census.Unopenable(inst.keyring)
	if len(missing) != 1 || missing[0] != 1 {
		t.Errorf("Unopenable = %v, want [1] — the operator has to be able to see this", missing)
	}

	// Restoring the key restores the workspace. Nothing was destroyed.
	inst.restartWith(ringOf(t, 2, 1, 2))
	if _, err := inst.resolver.ForWorkspace(context.Background(), stranded.PublicID()); err != nil {
		t.Errorf("restoring the key did not restore the workspace: %v", err)
	}
}

// TestKeyRotation_DoesNotChurnTheProviderCache.
//
// A master-key rotation re-encrypts the SAME provider secret, so the cached
// provider — which holds the plaintext and a live Keycloak service-account
// token — is still exactly right. Evicting it would make every affected
// workspace re-authenticate against Keycloak for nothing, all at once, which is
// the worst possible moment for an installation the operator is already
// changing.
//
// The cache key is `connection id @ updated_at` (resolver.cacheKey), so
// avoiding the churn comes down to rotation not touching updated_at — see the
// UpdateColumns note in connection/rotate.go.
//
// This is one half of a distinction. The other half —
// TestIsolation_RotatingTheSecretInPlaceIsPickedUp — proves that a NEW client
// secret DOES evict the cache, which it must, because that cached provider
// holds a credential the provider has revoked. Read together they say: the
// cache follows the credential, not the wrapping.
func TestKeyRotation_DoesNotChurnTheProviderCache(t *testing.T) {
	inst := newInstallation(t)
	inst.restartWith(ringOf(t, 1, 1))

	ws := inst.newWorkspace("cached")
	conn := inst.connectRealm(ws, "rt-cache-a", "primary")
	inst.kc.createUser("rt-cache-a", "cached-user")

	first := mustResolve(t, inst, ws).Provider
	if again := mustResolve(t, inst, ws).Provider; again != first {
		t.Fatal("the provider cache is not serving repeats; the rest of this test proves nothing")
	}

	// Rotation happens in another process, against the same database, while
	// this resolver keeps running and keeps its warm cache.
	inst.restartKeyringOnly(ringOf(t, 2, 1, 2))
	if _, err := connection.NewRotator(inst.db, inst.keyring).Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if got := storedSecret(t, inst.db, conn.ID).KeyVersion; got != 2 {
		t.Fatalf("the row did not rotate (v%d); the cache assertion below would be vacuous", got)
	}

	if afterRotation := mustResolve(t, inst, ws).Provider; afterRotation != first {
		t.Error("master-key rotation evicted the provider cache — every affected workspace " +
			"now re-fetches a service-account token from Keycloak for a secret that did not change")
	}

	// The cached provider is not merely the same pointer; it still works.
	if names := mustListUsers(t, inst, ws, "cached provider after rotation"); !contains(names, "cached-user") {
		t.Errorf("the cached provider stopped working after rotation: %v", names)
	}
}

// mustResolve resolves or fails.
func mustResolve(t *testing.T, i *installation, ws *workspace.Workspace) *Resolved {
	t.Helper()
	resolved, err := i.resolver.ForWorkspace(context.Background(), ws.PublicID())
	if err != nil {
		t.Fatalf("resolve %s: %v", ws.Slug, err)
	}
	return resolved
}

// restartKeyringOnly swaps the keyring used for ROTATION without rebuilding the
// resolver, modelling a rotation run by a separate process while this one keeps
// serving from its warm cache.
func (i *installation) restartKeyringOnly(ring *secrets.Keyring) {
	i.t.Helper()
	i.keyring = ring
}

// reSealUnderV1 rewrites one connection's secret under key version 1, to
// manufacture the state "this row was never rotated". It reads the plaintext
// through the CURRENT keyring first, so the row keeps working — the point is to
// change which key it needs, not to corrupt it.
func reSealUnderV1(t *testing.T, i *installation, connectionID string) {
	t.Helper()

	sealed, err := i.connections.OpenSecret(context.Background(), connectionID)
	if err != nil || sealed == nil {
		t.Fatalf("read sealed secret: %v", err)
	}
	plaintext, err := i.keyring.Open(*sealed, secretAAD(connectionID))
	if err != nil {
		t.Fatalf("open sealed secret: %v", err)
	}

	v1 := ringOf(t, 1, 1)
	reSealed, err := v1.Seal(plaintext, secretAAD(connectionID))
	if err != nil {
		t.Fatalf("re-seal under v1: %v", err)
	}
	err = i.db.Table("connections").Where("id = ?", connectionID).UpdateColumns(map[string]any{
		"secret_ciphertext":  reSealed.Ciphertext,
		"secret_nonce":       reSealed.Nonce,
		"secret_key_version": reSealed.KeyVersion,
		"secret_alg":         reSealed.Algorithm,
	}).Error
	if err != nil {
		t.Fatalf("write v1 secret: %v", err)
	}
}
