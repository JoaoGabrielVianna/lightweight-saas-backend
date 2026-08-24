# v0.5.0 Plan: Identity Experience Configuration

**Status:** **SCOPE FROZEN** 2026-08-23 · **Authoritative source for what v0.5.0 is**

> This document is the contract. [ROADMAP.md](ROADMAP.md) and
> [PRODUCT_DIRECTION.md](PRODUCT_DIRECTION.md) point here rather than restating
> it, so there is exactly one place where the scope can be read or changed.
> Where any other document disagrees with this one about v0.5.0, this one wins,
> and the code wins over all of them.

---

## 1. Product statement

A person who does not know Keycloak configures, from the LIGHTWEIGHT console
and per workspace, **how identity communicates and behaves**: where mail comes
from, what it says, whether self-registration exists, whether email is
verified, whether a password can be recovered. Every change is validated before
it is applied, audited after it is applied, and reversible where the provider
permits it.

**It does not include visual customization of the login and registration
pages.** That is deferred, and the reason is architectural rather than
effort. See [§4](#4-non-goals) and [§26](#26-deferred-capabilities).

## 2. Context and relevant evidence

Derived from the tree at `ced359e` during the v0.5.0 planning audit. The four
findings that shaped this scope:

1. **Email configuration is largely built and sits on the wrong surface.**
   `GET/PUT /admin/settings/smtp`, `POST /admin/settings/smtp/test` and
   `GET/PUT/DELETE /admin/settings/email-templates[/:key]` already exist, with
   console views. They are single-realm, run on the process-level
   `KEYCLOAK_*` client, have no dedicated tests, and emit no audit events.

2. **"Email templates" are Keycloak localization message-key overrides**, not
   FTL theme files. Six keys are exposed, covering invitation, password reset
   and email verification, subject and HTML body. The locale is hard-coded to
   `en`.

3. **A broken template can be saved today.** `UpdateEmailTemplate` accepts any
   string. Removing `${link}` from `passwordResetBodyHtml` ships a password
   reset email with no reset link, silently. The placeholder requirement exists
   only as prose in a field description.

4. **The Connection model cannot express configuration privilege.**
   `writeGrantRoles` is `{realm-admin, manage-users}` and deliberately excludes
   `manage-realm` ([TD-024](TECH_DEBT.md#td-024) explains why, for identity
   writes). But `PUT /admin/realms/{realm}`, which every realm setting needs,
   requires exactly `manage-realm`. `AccessMode` has no dimension for it. This
   is [TD-039](TECH_DEBT.md#td-039) and is the prerequisite for everything
   below.

## 3. The problem this version solves

Today an operator who wants to change the sender address on an invitation, turn
on email verification, or stop self-registration opens the Keycloak admin
console. That is a different product, a different vocabulary, and broader
access than the task warrants. LIGHTWEIGHT already removes the need for a
*backend* to hold Keycloak admin rights. This version removes the equivalent
need for a *person*, for the tasks that come up most.

## 4. Non-goals

- Becoming an email delivery service. Keycloak sends the mail; LIGHTWEIGHT
  never sits in the message path.
- Reproducing the Keycloak Admin Console.
- Visual login and registration customization.
- Any capability that works only against the bundled Keycloak.

## 5. MUST

The five items that define the version. All are workspace-scoped and
operator-only.

### M1. Connection configuration privilege

LIGHTWEIGHT must distinguish three capabilities of a connection: **read**,
**operational mutation** (which exists today), and **realm configuration**
(which does not).

- **Behaviour is fail-closed.** Absent or unproven configuration privilege
  means the configuration surface refuses before contacting the provider, and
  the console disables the controls and says why.
- **The representation is not frozen here.** A column, an enum extension, or a
  capability set are all acceptable; Slice 1 decides, and a better answer than
  the one imagined during planning is welcome.
- Must not weaken or reinterpret the existing `access_mode` contract, which
  [TD-024](TECH_DEBT.md#td-024) settled for identity writes.

**Acceptance:** a realm whose service account holds `manage-users` but not
`manage-realm` is refused configuration **before any provider call**; a realm
holding `realm-admin` is permitted; an unprovable grant is treated as absent.

### M2. Workspace-scoped SMTP configuration

Move SMTP configuration to the workspace-scoped `/v1` surface, resolved through
the calling workspace's active connection.

Must preserve, because they are already correct:

- the password is **write-only** across the API;
- it is redacted on read;
- placeholder-and-preserve semantics on save, so a save after a read never
  clears the password;
- a connection test that dials, negotiates TLS, authenticates and quits
  **without sending mail**;
- **Keycloak remains responsible for sending.**

Must add: workspace isolation, audit, tests, and refusal under M1.

**Acceptance:** two workspaces on two realms hold two different SMTP
configurations with no cross-read; the password never appears in any response,
log line or audit row.

### M3. Workspace-scoped email templates with validation

Move the supported message keys to the workspace-scoped surface, and validate
before writing.

Obligatory:

- know the supported keys and reject unknown ones;
- validate the **required placeholders per template type** before the write;
- return an error that names what is missing and where;
- **refuse the write when invalid**, rather than warn;
- apply only after validation passes;
- restore the default where the provider supports it (`DELETE` of the
  localization key already does this);
- audit the change, including enough of the previous value to reconstruct it.

**Persistent drafts are NOT a MUST.** See [§6](#6-should).

**Acceptance:** saving `passwordResetBodyHtml` without `${link}` is refused with
an error naming the placeholder; the previously stored value is unchanged;
restoring the default succeeds and is audited.

### M4. Identity experience realm settings

Expose at least: `registrationAllowed`, `verifyEmail`, `resetPasswordAllowed`,
`loginWithEmailAllowed`, `rememberMe`.

Each requires: current state read from the provider, change, validation,
authorization under M1, an audit event, console UI that explains **what changes
for the end user and where it is applied**, and acceptance against a real
Keycloak.

**Acceptance:** turning off `resetPasswordAllowed` removes the recovery path in
the real realm and the change is auditable; turning it back on restores it.

### M5. Configuration audit

Every mutation introduced by M2, M3 and M4 leaves a durable audit record with
actor, workspace, request id, outcome and enough of the prior state to
understand what changed.

- **Never includes a secret.** SMTP passwords are redacted at the audit
  boundary, not merely omitted by the caller.
- This is **not** audit investigation. It is the trail the new configuration
  surface owes, and nothing more. Investigation is
  [§37 of the roadmap direction](ROADMAP.md), targeted at v0.7.0.

**Acceptance:** changing the SMTP password produces an event that identifies
the change without containing the password, provable by a mutation test.

## 6. SHOULD

Recorded, not required. **None of these blocks the Definition of Done.**

- A real test email, using the existing `execute-actions-email` path, closing
  the last validation layer.
- `defaultLocale` and `supportedLocales`.
- Templates per locale. The locale is hard-coded to `en` today; **no MUST may
  depend on multi-locale**.
- Selecting a `loginTheme` that is already installed in the Keycloak.
- A richer draft and preview workflow.
- Formal deprecation of the `/admin/settings/*` routes, **if it is not required
  by a MUST**. Deprecation must not be a breaking change in v0.5.0.

## 7. COULD

SMTP provider presets · TOTP and required actions · additional login page text
via localization · a technical spike of the declarative theming model.

**Spike means:** prove or refute a technical premise. It does **not** mean
delivering a public capability, creating a stable contract, or adding a feature
quietly. A spike that produces shippable behaviour has failed its definition.

## 8. WON'T

Explicitly out, and each for a stated reason:

| Not in v0.5.0 | Why |
|---|---|
| Visual login/register builder | Not reachable through the Admin API for a Keycloak LIGHTWEIGHT does not own |
| Dynamic upload of Keycloak themes | No Admin REST endpoint exists for it; themes are files |
| Arbitrary HTML in login | That page receives the password. No sanitizer earns that trust |
| Custom JavaScript in login | Same reason, more directly |
| SMS OTP, phone as an auth channel, SMS gateway | Requires a custom Keycloak SPI and an external paid dependency |
| An email delivery service of our own | Keycloak sends. Staying out of the message path is what keeps this small |
| Audit investigation | Separate axis, targeted at v0.7.0 |
| Full ingestion of Keycloak events | Same axis |
| HA | [TD-027](TECH_DEBT.md#td-027), trigger has not fired |
| General architectural refactor | No MUST requires it |
| A general idempotency solution | [TD-036](TECH_DEBT.md#td-036); see [§29](#29-open-technical-questions) |
| Anything that works only against the bundled Keycloak | A capability without a contract for external installations is not a product capability |

## 9. Slices

| # | Slice | Objective | Complexity |
|---|---|---|:--:|
| 1 | **Connection configuration privilege** | M1. Distinguish read, operational mutation and realm configuration. Fail-closed | M |
| 2 | **SMTP behind the port** | Promote SMTP off the concrete provider onto `identity.IdentityProvider` and a service, with tests | M |
| 3 | **Workspace-scoped SMTP** | M2. `/v1` surface, console, isolation | M |
| 4 | **Template validation and workspace scope** | M3. Validator, refusal, restore-to-default | L |
| 5 | **Realm experience settings** | M4. Five settings, explanatory UI | M |
| 6 | **Configuration audit** | M5. Canonical actions, redaction | S |

**Stop criterion for Slice 4.** If it exceeds its timebox, ship structural
validation and refusal alone and defer everything editorial. A partial
validator that refuses a broken template is worth more than none.

## 10. Dependencies between slices

```
1 ──▶ 3 ──▶ 4
│     ▲     │
│     │     ▼
│     2     6
│           ▲
└──▶ 5 ─────┘
```

- **1 gates everything.** If Slice 1 shows the privilege model cannot express
  configuration capability, the version is re-planned rather than continued.
- **2 before 3.** Moving SMTP behind the port first keeps the `/v1` handler
  from being written against a concrete adapter. It also removes `server` as an
  importer of the Keycloak adapter, which narrows the boundary rather than
  widening it.
- **6 last**, because it audits what 3, 4 and 5 create.

## 11. Security invariants

1. Configuration is **operator-only**. No project credential reaches it, and no
   new scope is introduced.
2. Refusal happens **before** the provider is contacted, as the existing
   authorization boundary already does.
3. Absent or unprovable privilege is **denial**, never a permitted attempt.
4. Validation runs **before** the write, never as a warning after it.
5. **No arbitrary HTML, and no JavaScript, reaches a login page** through this
   version. The template surface stays the closed set of message keys.
6. Configuring the **installation's own realm** must be protected against
   operator lockout. The mechanism is not designed here; the requirement is
   binding, and the slice that touches it must propose one before implementing.

## 12. Tenant-isolation invariants

1. Configuration is resolved through the calling workspace's active connection,
   never through process-level `KEYCLOAK_*`.
2. A workspace cannot read or write another workspace's configuration, and this
   is proven against two real realms rather than asserted.
3. A resource id from one workspace cannot act through another, matching the
   existing cross-realm guarantee.
4. One workspace's provider being unreachable does not degrade another's
   configuration surface.

## 13. Secret-handling invariants

1. **The SMTP password is not stored by LIGHTWEIGHT.** It lives in the
   Keycloak realm. A second copy would add a synchronization problem and
   leak surface for no gain.
2. **The keyring is not extended to SMTP.** `SECRETS_KEYRING` continues to seal
   connection client secrets only.
3. The password is write-only across the API: redacted on read, preserved on
   save when the caller returns the placeholder.
4. Secrets never reach a log line, an audit row, an error message, or a
   diagnostic artifact.
5. The console must state plainly that the password cannot be shown again.

## 14. Provider and Keycloak assumptions

Recorded so they can be re-checked rather than assumed:

| Assumption | Basis | Status |
|---|---|---|
| Target is Keycloak 26.0 | compose, CI, integration suite | proven |
| `PUT /admin/realms/{realm}` applies a partial update | in use today for `smtpServer` | proven |
| Realm settings writes require `manage-realm` | Keycloak role semantics | to confirm in Slice 1 |
| Localization overrides need internationalization enabled | handled today by `EnableInternationalizationIfNeeded` | proven |
| Deleting a localization key reverts to the theme default | in use today | proven |
| There is **no** Admin REST endpoint to upload a theme | Keycloak deploys themes as files or providers | inferred, high confidence |

**If any assumption marked "to confirm" or "inferred" turns out false, stop and
report before proceeding.** A plan built on a wrong provider assumption is worse
than a plan that pauses.

## 15. Existing-Keycloak compatibility

This is the primary journey and the binding constraint.

- SMTP, localization and realm settings are all reachable through the Admin
  API, so all of M2 to M4 work against a Keycloak LIGHTWEIGHT does not own.
- They require `manage-realm` on the connection's service account, which
  today's documented least-privilege set does not include. **M1 must make that
  requirement visible and refusable rather than a silent 403**, and the
  installation documentation must state it.
- Nothing in this version requires filesystem access to the Keycloak, a
  restart, or a custom provider.

## 16. Bundled-Keycloak compatibility

Everything works, because the bundled realm grants `realm-admin`. This is
**not** a reason to rely on it: a capability that works only there has no
contract for real installations and is excluded by [§8](#8-wont).

## 17. Validation strategy

Five layers, with what each cannot prove stated, because a validator that
overclaims is worse than none.

| Layer | Proves | Does not prove |
|---|---|---|
| **Structural** | required placeholders present, key known, size bounded | that it renders, or that the link works |
| **Security** | no script, no event handler, no `javascript:`, no external resource, no form | semantic attacks, or phishing by wording |
| **Rendering** | the markup parses | that it is legible or accessible |
| **Functional** | required elements survive a synthetic render | that the real provider accepts it |
| **Real acceptance** | a real message arrives and its link authenticates | anything beyond the case exercised |

**The MUST is the structural layer plus refusal.** It is what closes the defect
that exists today. Security-layer checks are included where they are cheap and
unambiguous. Layers 4 and 5 are SHOULD.

## 18. Audit requirements

- A canonical action per configuration mutation, following the existing
  vocabulary in `internal/audit`.
- Workspace-scoped, durable, in the same trail as the rest.
- Records actor, request id, outcome, and enough prior state to understand the
  change.
- **Redaction is enforced at the audit boundary**, not delegated to callers.
- Provider mutations remain non-atomic with their audit row
  ([TD-038](TECH_DEBT.md#td-038)); this version does not change that and must
  not imply otherwise.

## 19. Migration strategy

- Additive only. At most one column on `connections` for M1.
- No new table is anticipated. If one becomes necessary, that is a scope
  question, so stop and report.
- Reversible down migration, matching the existing convention.
- Idempotent, matching migrations `000001` through `000006`.

## 20. Rollback strategy

Per layer, and stated honestly where it does not exist:

| Surface | Rollback |
|---|---|
| Email template | Restore the default by deleting the localization key. Previous value recoverable from the audit event |
| Realm setting | Previous value recorded in the audit event; reapply |
| SMTP configuration | **No automatic rollback.** The console must say so before saving |
| Schema | Down migration |

## 21. Acceptance strategy

Real stack, following the pattern already established for the authorization
matrix and the two-realm demo:

- Two real realms with different service-account privileges.
- Configuration isolation proven across them.
- Fail-closed refusal proven **before** any provider call.
- A real message flow for the SHOULD test-email item, if it ships.

## 22. Mutation strategy

The gate must go red when the behaviour breaks. At minimum:

- remove each required placeholder check, one at a time;
- accept an unknown template key;
- accept a script tag;
- treat unprovable configuration privilege as permitted;
- drop redaction from the audit path;
- allow cross-workspace configuration read.

Each break must be caught, matching the existing
`authz-mutation-check.sh` and `audit-mutation-check.sh` pattern.

## 23. Documentation requirements

- This document stays the authoritative scope.
- Installation documentation states the `manage-realm` requirement and what is
  refused without it.
- A user-facing guide for the configuration journey, in `getting-started/` or
  `operations/`, decided when the surface is known.
- Troubleshooting entries for the new refusals, with symptoms the code can
  actually produce.
- `FEATURES.md`, `ARCHITECTURE.md` and `PROJECT_STATUS.md` are updated **when
  the code lands**, not before.

## 24. Definition of Done

See [§25](#25-v050-acabou). The technical gates are:

all MUST implemented · unit tests · integration tests against a real Keycloak ·
tenant isolation proven · negative authorization proven · configuration
privilege fail-closed proven · secrets redaction proven · audit events present ·
validator negative cases proven · migration reversibility proven · OpenAPI
regenerated with no drift · frontend tests · browser end-to-end for the main
journeys · coverage at or above the existing floors · applicable mutation gates
green · documentation updated · upgrade and rollback documented · `make ci`
green · remote CI green · CodeQL green · release checklist complete, including
the human documentation gates 2.6, 2.7 and 2.8.

**No floor may be lowered to publish.**

## 25. v0.5.0 ACABOU

**When every MUST and its acceptance criteria are satisfied, and the release
gates are green, v0.5.0 is functionally closed.**

**Incomplete SHOULD and COULD items do not block the release.**

After the freeze:

- a bug or a compatible correction goes to **v0.5.x**;
- a new capability goes to **v0.6.0 or later**.

**No new idea enters MUST during implementation without all three of:**

1. proof that an approved MUST is impossible or unsafe without it;
2. an explicit written report of that proof;
3. human authorization before the scope expands.

This rule applies to good ideas, to small ideas, and to ideas that arrive with
a convincing argument. The argument is what step 2 is for.

## 26. Deferred capabilities

| Capability | Target | Reason |
|---|---|---|
| Visual login and registration customization | v0.6.0 direction | Needs a declarative model with a one-time provider installation. Not reachable through the Admin API alone |
| Audit investigation | v0.7.0 direction | Separate axis; mixing two large axes is what prevents closing a version |
| SMS, phone, OTP by message | unscheduled | Custom Keycloak SPI plus an external dependency |
| Idempotency keys | unscheduled | [TD-036](TECH_DEBT.md#td-036); see [§29](#29-open-technical-questions) |

## 27. Known risks

| Risk | Likelihood | Impact | Mitigation |
|---|:--:|:--:|---|
| Becoming a fragile wrapper over Keycloak | high | high | Expose only what is stable in the Admin API; never reimplement the provider |
| A configuration change breaks sign-in | medium | high | Validate before write; explicit warnings on the settings that remove a user path |
| **Operator lockout on the installation realm** | low | **critical** | Binding requirement in [§11](#11-security-invariants); mechanism proposed before implementation |
| Keycloak version differences | medium | medium | Pinned to 26.0; assumptions table in [§14](#14-provider-and-keycloak-assumptions); explicit failure on unknown fields |
| Secret leakage | low | high | Redaction at the audit boundary, proven by mutation |
| Promising more "no-code" than is delivered | high | high | Console and documentation state that visual theming is not included |
| Slice 4 growing into an editorial subsystem | high | medium | Stop criterion in [§9](#9-slices); drafts are not a MUST |
| Partial configuration or provider drift | medium | medium | Read after write; the console shows the provider's actual state |

## 28. Human decisions already resolved

Settled on 2026-08-23. **These are closed. Reopening one is a scope change.**

| # | Decision | Resolution |
|---|---|---|
| D1 | Visual theming in v0.5.0 | **No.** Technical spike only, with no public capability |
| D2 | Arbitrary HTML in login | **No.** Future direction is a closed declarative domain |
| D3 | SMS and phone | **Later.** Not in v0.5.0 |
| D4 | Audit investigation | **v0.7.0.** Only configuration audit here |
| D5 | `/admin/settings/*` | **Deprecate, do not remove.** No breaking change in v0.5.0 |
| D6 | SMTP presets | **Generic SMTP is the contract.** Presets are COULD and cannot block |
| D7 | Idempotency | **Out**, unless proven to be a correctness or security requirement of a MUST |
| D8 | Installation's own realm | **Configurable, with explicit lockout protection.** Mechanism proposed in-slice |
| D9 | Templates per locale | **SHOULD.** No MUST may depend on multi-locale |
| — | Draft/Preview/Publish/Rollback workflow | **Not a MUST.** Validate-and-refuse is the requirement |

## 29. Open technical questions

Answerable only during implementation. Each has a defined stopping point.

1. **Does a realm settings write require `manage-realm` specifically, or does
   another grant suffice?** Slice 1 answers it empirically. If the privilege
   model cannot express it, stop and re-plan.
2. **What is the right representation for configuration capability?** Column,
   enum extension, or capability set. Slice 1 decides; not frozen here.
3. **Which placeholders are genuinely required per template key?** Derived from
   Keycloak's default bundle, not guessed. A missing requirement is a false
   negative; an invented one refuses valid templates.
4. **Can a template be validated without rendering it?** If structural
   validation proves insufficient, say so rather than overclaiming.
5. **What protects the installation realm from lockout?** Proposed before
   implementation, per [§11](#11-security-invariants).
6. **Does any MUST turn out to need idempotency to be correct or safe?** If a
   lost response can leave configuration in a state the caller cannot reconcile,
   that is the D7 exception: **stop and report before expanding scope.**

---

## See also

- [ROADMAP.md](ROADMAP.md) for where this sits in the sequence
- [PRODUCT_DIRECTION.md](PRODUCT_DIRECTION.md) for why the product has this shape
- [TECH_DEBT.md](TECH_DEBT.md) and [KNOWN_ISSUES.md](KNOWN_ISSUES.md) for what is wrong today
