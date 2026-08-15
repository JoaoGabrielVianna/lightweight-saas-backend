# Master-Key Rotation

How to change the key that encrypts identity-provider credentials at rest,
without re-entering a single one by hand and without downtime.

Resolves [TD-019](TECH_DEBT.md#td-019).

---

## 1. What is encrypted, and with what

A [Connection](CONNECTIONS.md) holds one identity provider's client secret. That
secret is never stored in plaintext: it is sealed with AES-256-GCM before it
reaches the `connections` table, and the row keeps four columns describing the
result.

| Column | Holds |
|---|---|
| `secret_ciphertext` | the sealed credential |
| `secret_nonce` | the 96-bit nonce it was sealed with |
| `secret_alg` | `aes-256-gcm` |
| `secret_key_version` | **which master key opens it** |

That last column is what makes rotation possible. A row is opened with the key
its own version names, and with no other — there is no "try every key until one
works" path, deliberately. See
[internal/secrets/keyring.go](../internal/secrets/keyring.go) for why.

**The master keys are not in the database.** That is the entire point: a stolen
`pg_dump` is useless without them. It also means a database backup alone does
not restore a working installation — see §7.

---

## 2. Configuration

```bash
SECRETS_KEYRING=1:<base64 key>,2:<base64 key>
SECRETS_KEY_CURRENT=2
```

* Every version in `SECRETS_KEYRING` can **decrypt**.
* Exactly one — `SECRETS_KEY_CURRENT` — **encrypts** everything new.
* Versions are explicit, start at 1, and may not repeat.
* `SECRETS_KEY_CURRENT` is optional when exactly one key is configured, and
  required with more than one. "The highest version" is a plausible rule that is
  wrong exactly once, during a rollback, so it is not inferred.

Generate a key with `make secrets-genkey` (`openssl rand -base64 32`).

### Upgrading from `SECRETS_MASTER_KEY`

The legacy variable still works and maps to **version 1** — the version every
existing row already carries, since the schema has defaulted `secret_key_version`
to 1 since migration 000003 and the pre-rotation sealer stamped nothing else.

```bash
# before
SECRETS_MASTER_KEY=abc…

# after — identical behaviour, and now rotatable
SECRETS_KEYRING=1:abc…
```

Setting **both** is refused at boot. There is no ordering of the two that is
obviously right, and guessing produces exactly the failure this feature exists
to prevent.

---

## 3. Rotating

### Step 1 — add the new key, keep the old one

```bash
SECRETS_KEYRING=1:<old>,2:<new>
SECRETS_KEY_CURRENT=2
```

Restart the API. Nothing has been re-encrypted yet: existing rows still say v1
and are still opened with v1, while anything created from now on is sealed with
v2. **The installation is fully working in this state** — that is what makes the
rotation zero-downtime, and it is covered by
`TestKeyRotation_ConnectionSurvivesTheWholeLifecycle`.

Check what is ahead of you:

```bash
make secrets-rotate-dry-run
```

```
Dry run. Nothing was decrypted and nothing was written.

Current key:       v2
Total secrets:     14
Already current:   0
Would rotate:      14
```

A dry run reads the stored key **version** of each row and nothing else. It
cannot tell you that the key configured for a version is the *right* one —
proving that means running AES-GCM over the row, which is the real run.

### Step 2 — rotate

```bash
make secrets-rotate
```

```
Current key:       v2
total:             14
already_current:   0
rotated:           14
failed:            0
```

Each row is re-sealed in its own transaction under a row lock. The command is
**idempotent** — rows already on the current key are counted and skipped, not
re-encrypted — and **resumable**: if it is interrupted or a row fails, the rows
that committed stay committed and re-running picks up the rest.

Exit codes:

| Code | Meaning |
|---|---|
| `0` | success, including "nothing needed rotating" |
| `1` | a row failed, or a persisted key version is not configured |
| `2` | bad invocation or bad configuration; nothing was attempted |

### Step 3 — confirm nothing needs the old key

```bash
make secrets-status
```

```
Keyring versions:  v1, v2 (current)
Current key:       v2

Persisted connection secrets by key version:
  v2       14  ← current

Safe to remove:    v1
```

**Do not skip this.** It is the only thing that distinguishes "rotation
finished" from "rotation mostly finished".

### Step 4 — remove the old key

```bash
SECRETS_KEYRING=2:<new>
SECRETS_KEY_CURRENT=2
```

Restart. Destroy the old key material only after §7's backup rule is satisfied.

---

## 4. When something goes wrong

Both failures leave the row **exactly as it was**, holding valid ciphertext.
Nothing is destroyed by a failed rotation.

### `missing_key_version`

The row names a version that is not in `SECRETS_KEYRING`.

```
conn_3f2504e0-… (sealed under v1): missing_key_version
```

Restore that key material to the keyring and re-run. Rows already rotated are
skipped. Until then, the affected workspaces answer `credentials_unavailable`;
**every other workspace keeps working** — see §6.

### `cannot_open`

The version *is* configured, and the key behind it does not open the row. Either
the wrong material was pasted for that version, or the row was altered. AES-GCM
authentication failed, and no other key was tried.

This is not fixed by restarting. Find the correct key material, or replace the
connection's credential through the API (`PATCH /v1/workspaces/{id}/connections/{id}`
with a new `client_secret`).

---

## 5. Master-key rotation is not credential rotation

| | changes | evicts the provider cache | Keycloak involved |
|---|---|---|---|
| **Master-key rotation** (`secrets rotate`) | the wrapping | **no** | no |
| **Credential rotation** (`PATCH … client_secret`) | the credential | **yes** | yes, you issue a new secret there |

The runtime caches one provider per connection, keyed on `id@updated_at`. A
master-key rotation deliberately does **not** touch `updated_at`: the plaintext
credential is unchanged, so the cached provider — and its live service-account
token — is still exactly right, and evicting it would make every affected
workspace re-authenticate against Keycloak for nothing, all at once, in the
middle of a change the operator is already making.

A new client secret does bump `updated_at` and must, because that cached
provider holds a credential the provider has revoked.

---

## 6. What a missing key does *not* do

It does not take the instance out of the load balancer.

`/health/ready` reports **global** dependencies only. A missing historical key
strands the connections sealed under it and nothing else; failing readiness
would take every other tenant down with them, plus `/v1/projects`,
`/v1/workspaces` and the audit API, none of which involve a key at all. The
condition is loud without being fatal:

* an `ERROR` line at boot and every five minutes, naming the versions and how
  many credentials need them;
* `lightweight_secret_key_version_rows{version="1"}` — rows still needing each
  version, the number to watch a rotation finish by;
* `lightweight_secret_open_failures_total{reason="unknown_key_version"}` — a
  request that actually hit the problem;
* `make secrets-status`, exiting non-zero.

A keyring that cannot be **built** at all — malformed key, a current version not
in the ring — is a different thing and does refuse the boot. "The operator typed
the configuration wrong" is fatal; "the data needs a key the configuration does
not have" is visible and survivable.

---

## 7. Backup

> **A `pg_dump` alone does not restore a working installation.**

```
database backup  +  keyring backup  =  recoverable installation
```

* Back up `SECRETS_KEYRING` **separately from the database**, and never inside
  the dump. Storing the key beside the ciphertext it protects removes the
  protection.
* Keep **every key version any retained backup might need**, not just the
  current one. A dump taken before a rotation contains v1 rows; restoring it
  into a process holding only v2 gives you unreadable credentials.
* Therefore: `secrets status` reporting `Safe to remove: v1` means *no live row
  needs v1*. It does not mean no **backup** needs it. Destroy old key material
  only once every dump that could still be restored is past its retention
  window.

Losing a key version with no copy is unrecoverable — the affected connections
must be re-created with fresh provider credentials. That is not a bug to be
fixed later; it is what encryption at rest means.

See [operations/BACKUP_AND_RECOVERY.md](operations/BACKUP_AND_RECOVERY.md) §2.6.

---

## 8. Docker / VPS

Both variables are ordinary environment variables. `docker-compose.yml` forwards
them to the `api` service; a keyring set in `.env` but not forwarded is an
installation that silently keeps using the old key, which is why the compose
file names them explicitly.

```bash
# .env
SECRETS_KEYRING=1:<old>,2:<new>
SECRETS_KEY_CURRENT=2
```

```bash
docker-compose up -d api          # the API picks up both keys
make secrets-rotate               # runs against DB_URL from the same .env
make secrets-status               # confirm before editing .env again
```

A managed secret store (systemd `LoadCredential`, a VPS secret manager, Docker
secrets rendered into the environment) is a stronger production choice and works
unchanged — the process reads two environment variables and does not care how
they got there. None of it is required.

**No external KMS.** AWS KMS, GCP KMS, Vault and HSMs are all deliberately out
of scope. The keyring abstraction leaves room for one later; this is local
master-key lifecycle.

---

## 9. Verifying the whole thing

```bash
DB_URL=postgres://…/throwaway make secrets-check
```

Drives the compiled CLI through a full lifecycle against a real PostgreSQL —
legacy variable, mixed keys, rotation, idempotent re-run, old key removed, every
refusal path — and then scans everything it produced for the keys and provider
secrets it used. Zero matches is the pass condition.

---

## See also

* [CONNECTIONS.md](CONNECTIONS.md) — the Connection lifecycle
* [WORKSPACE_IDENTITY_RUNTIME.md](WORKSPACE_IDENTITY_RUNTIME.md) — how a sealed credential becomes a provider
* [operations/BACKUP_AND_RECOVERY.md](operations/BACKUP_AND_RECOVERY.md) — the rest of the backup model
* [TECH_DEBT.md](TECH_DEBT.md) — TD-019
