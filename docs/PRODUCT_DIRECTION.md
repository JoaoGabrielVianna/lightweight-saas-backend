# Product Direction

**Last updated:** 2026-08-24 · Companion to [ROADMAP.md](ROADMAP.md) and
[PROJECT_STATUS.md](PROJECT_STATUS.md)

**What this document is.** An explanation of what LIGHTWEIGHT is *for*, and
where its edges are. It is the conceptual frame the other documents hang off.

**What this document is not.** Not a backlog, not a specification, not a
commitment. Nothing here schedules work. Sequence lives in
[ROADMAP.md](ROADMAP.md); what is broken lives in
[TECH_DEBT.md](TECH_DEBT.md) and [KNOWN_ISSUES.md](KNOWN_ISSUES.md).

**How to read the labels.** Every item below carries one, and they are not
degrees of the same thing:

| Label | Meaning |
|---|---|
| **AVAILABLE TODAY** | Exists in the shipped product, with a code reference or a document that has one. You can use it now. |
| **DIRECTION** | Where this pillar is heading. Not designed, not sized, not scheduled, not promised. |
| **FUTURE** | A recognised idea with no committed shape and no version attached. Further out than DIRECTION. |

If a reader can mistake a DIRECTION item for something they can use, this
document has failed. That failure is what the v0.4.2 documentation
reconciliation existed to clear, and it is worth not reintroducing here.

---

## The one-sentence version

LIGHTWEIGHT puts one or more Keycloak realms behind a single workspace-scoped
API, so that **services and people can do identity work at the level of
privilege the task actually needs**, instead of at the level Keycloak's admin
roles happen to grant.

Everything below is a consequence of that sentence.

---

## The boundary with Keycloak

This is the most important thing on the page, and the thing most likely to be
misread as the product grows.

**Keycloak is the identity provider and the identity runtime.** It stores users,
hashes passwords, issues and signs tokens, holds sessions, runs authentication
flows, and sends mail. LIGHTWEIGHT stores none of that and does none of it.
There is no login endpoint in Go, no JWT signing in Go, no password handling in
Go. This is [AD-001](PROJECT_STATUS.md#ad-001--keycloak-owns-identity), and it
is not under review.

**LIGHTWEIGHT owns the boundary in front of it**: who may ask for what, on
behalf of which tenant, and what was recorded about it.

The practical test, applicable to any future feature:

| If the change would... | Then |
|---|---|
| let a caller do something with *less* privilege than Keycloak would require | it is in scope |
| make a common operator task safe, scoped, audited, or reachable without provider admin rights | it is in scope |
| require reimplementing something Keycloak already does as its runtime | it is out of scope |
| require LIGHTWEIGHT to become the source of truth for identity data | it is out of scope, and contradicts AD-001 |

**LIGHTWEIGHT does not aim to reproduce the Keycloak Admin Console.** That is a
deliberate non-goal, not a gap waiting to be filled. Keycloak's console is a
complete administrative surface for a complete identity server, and competing
with it would mean either a permanent chase or a worse version of it. Where an
operator needs the full surface, the right answer is Keycloak's own console.

What LIGHTWEIGHT offers instead is **narrower and safer**: the operations a
tenant-scoped product actually needs, expressed in a vocabulary that does not
require knowing what a realm is, with an audit trail and a scope model that
Keycloak's coarse admin roles cannot express.

---

## 1. Identity

*Who exists, what they may do, and whether they are still allowed to do it.*

This pillar is the most complete. It is what v0.1 through v0.4 built.

### AVAILABLE TODAY

| Capability | Reference |
|---|---|
| Users: create, read, update, delete, per workspace | [WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md) |
| Roles: create, read, update, delete, and membership | same |
| Sessions: list per realm and per user, revoke one | same |
| Invitations: list, create, revoke, resend | same |
| Password reset by email, and direct set | same |
| Authentication: OIDC / PKCE, delegated entirely to Keycloak | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Token validation: JWKS, asymmetric algorithms only, `iss` and `azp` and `exp` enforced | same |
| Live-admin check, closing the stale-JWT revocation window, fail-closed | [AD-003](PROJECT_STATUS.md#ad-003--live-admin-check-overrides-the-jwt-claim-failing-closed) |
| Self-protection guards: self-delete, self-disable, self-strip-admin, last-admin | [KI-018](KNOWN_ISSUES.md#ki-018) |
| Multi-realm routing: each workspace resolved to its own realm per request | [WORKSPACE_IDENTITY_RUNTIME.md](WORKSPACE_IDENTITY_RUNTIME.md) |

### Known limits, stated rather than implied

- **Realm-wide session revocation does not exist.** There is no "log everyone
  out" button; the console's Sessions tab carries a `coming-soon` flag
  ([KI-006](KNOWN_ISSUES.md#ki-006)).
- **A bearer token outlives an OIDC logout** until it expires. This is the
  standard stateless-JWT trade-off, accepted deliberately
  ([KI-002](KNOWN_ISSUES.md#ki-002)), and partially mitigated for admins by the
  live-admin check.
- **Four list endpoints do not paginate**, and hard-cap truncation is silent
  ([TD-007](TECH_DEBT.md#td-007), [KI-013](KNOWN_ISSUES.md#ki-013)).

### DIRECTION

Identity administration is close to complete for what this product claims. The
direction here is **consolidation rather than expansion**: one identity surface
instead of two. Today `/admin/*` and `/v1` are two code paths to the same
operations, with two error envelopes, and the console still uses the older one
([TD-022](TECH_DEBT.md#td-022)).

No new identity nouns are currently intended.

---

## 2. Experience

*What a person actually sees and receives when they sign in, get invited, or
reset a password.*

This pillar is the least developed, and it is the one the next release is aimed
at.

### AVAILABLE TODAY

| Capability | Where | Caveat |
|---|---|---|
| SMTP configuration for the installation's realm | `/admin/*` | No `/v1` equivalent; no dedicated Go tests |
| SMTP connection test | `/admin/*` | same |
| Keycloak email-template customization | `/admin/*` | same |
| A custom FTL email theme | `deploy/keycloak/themes/` | Does not survive container recreation ([KI-011](KNOWN_ISSUES.md#ki-011)) |
| Console localization, including PT-BR | admin console | The console only, not the end-user login screens |

**Read that table carefully before assuming this pillar is empty or full.** The
capabilities exist, but they sit on the legacy operator surface, they are
single-realm, they are untested, and one of them does not persist. Why they were
left there is recorded in
[WORKSPACE_IDENTITY_API.md §7](WORKSPACE_IDENTITY_API.md#why-smtp-and-email-templates-are-deferred):
they are provider-specific realm settings rather than identity administration,
and moving them means designing that seam rather than moving a handler.

### DIRECTION

**Make common identity-experience configuration reachable by someone who should
not have to learn Keycloak to change it.**

The gap this responds to is concrete. LIGHTWEIGHT already means a *backend* does
not need Keycloak admin rights. It does not yet mean the same for a *person*. An
operator who wants to change the sender address on an invitation, or the wording
of a login screen, is currently sent to a different console with a different
vocabulary and broader access than the task warrants.

Areas under consideration, in no committed order:

- email delivery and configuration
- email templates
- the authentication experience
- login and registration customization
- branding
- no-code configuration for the common cases
- validated customization for the cases that need more

### What is NOT decided

**The scope of this pillar's first release is not frozen.** Specifically, none
of the following has been chosen, and none should be inferred: an HTML format or
markup subset, a DSL, a component model, a schema or migration, an endpoint
shape, a draft/publish model, version history, a preview or validation engine,
rollback semantics, a supported-provider list, MFA, a number of configurable
pages, or any new scope or role.

Those are the subject of the v0.5.0 design sprint, which has not happened. See
[ROADMAP.md § NEXT](ROADMAP.md#next--v050--identity-experience).

**The boundary still applies.** Anything built here configures Keycloak's
experience. It does not replace Keycloak's authentication flows, its template
engine, or its runtime.

---

## 3. Governance

*Who did what, on whose behalf, with what authority, and can it be proven later.*

### AVAILABLE TODAY

| Capability | Reference |
|---|---|
| Projects: a backend consuming the API on behalf of exactly one workspace | [PROJECTS.md](PROJECTS.md) |
| Credentials: `lw_sk_` keys, digest-only at rest, shown once, revocable, optionally expiring | same |
| Scopes: 9, explicit per credential, least privilege that is actually enforceable | same |
| A permanent workspace binding as the authorization boundary, checked before any provider is touched | [security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md) |
| A route registry the process refuses to boot without: every `/v1` route carries an authorization classification | same |
| Durable, workspace-scoped audit trail in PostgreSQL, 28 canonical actions | [AUDIT.md](AUDIT.md) |
| Control-plane mutations written in the same transaction as the change they record | [TD-033](TECH_DEBT.md#td-033) |
| Audit readable per workspace by an operator or a credential holding `audit:read` | [AUDIT.md](AUDIT.md) |
| Provider credentials sealed at rest: AES-256-GCM, per-row AAD, versioned keyring, online rotation | [SECRET_KEY_ROTATION.md](SECRET_KEY_ROTATION.md) |
| Retention by age, `AUDIT_RETENTION_DAYS` | [AUDIT.md](AUDIT.md) |

### Known limits, stated rather than implied

- **Provider mutations are not atomic with their audit row.** A user created in
  Keycloak whose audit write then fails leaves a change without a record. No
  PostgreSQL transaction can prevent that, and failing the response instead
  would invite a retry that creates a second user
  ([TD-038](TECH_DEBT.md#td-038)).
- **Authorization refusals do not reach the durable trail.** A `403` produces a
  log line and a security event, not an audit row
  ([TD-037](TECH_DEBT.md#td-037)).
- **There are no idempotency keys**, so a client whose response was lost cannot
  safely retry a mutation ([TD-036](TECH_DEBT.md#td-036)).
- **Authorization is currently operator-global for `/v1`.** Per-workspace
  authorization does not exist yet; the router comment records that when it
  arrives it will tighten, never loosen.

### FUTURE

**Audit and investigation.** The trail today answers "what happened to this
workspace". The direction is to make security-relevant events *answerable*
rather than merely recorded: visibility, and the ability to investigate.

**No version is attached to this, deliberately.** The analysis that exists
already shows why it is not a small step.
[TD-037](TECH_DEBT.md#td-037) carries the arithmetic: refusals cannot simply be
added to the existing table, because a single misconfigured backend retrying a
`403` in a loop would outweigh the real history by roughly four orders of
magnitude under one age-based retention policy. A separate security-event class
with its own retention, volume controls and read surface is named there as the
eventual answer, and doing it badly would degrade the trail the product's audit
story now rests on.

That is a reason to record the direction and wait, not a reason to schedule it.

---

## What LIGHTWEIGHT is deliberately not

Listed so the absence reads as a decision rather than a gap:

| Not this | Why |
|---|---|
| **Your application backend** | It stores no product data and serves no product endpoints |
| **An identity provider** | Keycloak is. LIGHTWEIGHT holds no credentials of record and signs no tokens (AD-001) |
| **A replacement for the Keycloak Admin Console** | A narrower, safer surface is the point; parity would be a permanent chase |
| **A general SaaS backend** | No billing, no queue, no object storage, no organizations. See [FEATURES.md §12](FEATURES.md#12-not-started--product-domain) |
| **Horizontally scalable, today** | Single instance is what is supported and claimed. The blocker is [TD-027](TECH_DEBT.md#td-027) |

---

## See also

- [ROADMAP.md](ROADMAP.md) — sequence, and what is explicitly not promised
- [PROJECT_STATUS.md](PROJECT_STATUS.md) — the current state, with numbers
- [FEATURES.md](FEATURES.md) — what exists, with a code reference per claim
- [ARCHITECTURE.md](ARCHITECTURE.md) — how the boundary is actually built
- [getting-started/](getting-started/README.md) — installing and using it
