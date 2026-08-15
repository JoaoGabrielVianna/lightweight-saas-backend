# Release Checklist

**Last updated:** 2026-07-27 · Companion to [QUALITY_GATE.md](QUALITY_GATE.md)

The standing, version-agnostic process for cutting a release. Run it top to
bottom; stop at the first failure.

> **Supersedes [release/RELEASE_CHECKLIST.md](release/RELEASE_CHECKLIST.md)**,
> which was written for the v0.2.0 agent-driven milestone and hardcodes that
> release's branch names, owners, and per-agent evidence layout. It is kept as
> a historical record of how v0.2.0 shipped.

**Versioning:** [Semantic Versioning](https://semver.org/). Pre-1.0, minor
bumps may break — breaking changes always get a `### Breaking` subsection in
the changelog.

> ### This checklist does not release the Go SDK
>
> `sdk/go` is a **separate Go module with a separate tag namespace**, and the two
> releases do not imply each other in either direction:
>
> | | server | Go SDK |
> |---|---|---|
> | tag | `v0.4.0` | `sdk/go/v0.1.0` |
> | consumers install | — | `go get …/sdk/go@v0.1.0` |
> | changelog | [CHANGELOG.md](../CHANGELOG.md) | [sdk/go/CHANGELOG.md](../sdk/go/CHANGELOG.md) |
> | process | this document | [SDK_GO.md § Release](SDK_GO.md#release) |
>
> Cutting `v0.4.0` publishes **nothing** for an SDK consumer: Go resolves a
> nested module only from a tag carrying its directory as a prefix, so the
> server's tags are in a namespace `sdk/go` cannot be reached from. If a slice
> changed the SDK's exported surface, it needs its own release, with its own
> version, decided on its own merits.

---

## Phase 0 — Decide the version

| # | Gate | How |
|---|---|---|
| 0.1 | Working tree clean, on the release branch | `git status --porcelain` is empty |
| 0.2 | Every intended change is merged | `git log $(git describe --tags --abbrev=0)..HEAD --oneline` |
| 0.3 | Version number chosen and justified | See the table below |

| Change in the diff | Bump |
|---|---|
| Breaking API or config change | **minor** while pre-1.0 (major after) |
| New capability, backwards-compatible | **minor** |
| Fixes, docs, internal refactor only | **patch** |

Record the reasoning in the CHANGELOG entry — "why this number" is worth one
sentence.

---

## Phase 1 — Automated gates

All of these must be green. None of them require judgement.

| # | Gate | Command | Expect |
|---|---|---|---|
| 1.1 | Code health | `make ci` | `+ CI checks passed` |
| 1.2 | Coverage floor | `make coverage-gate` | at or above the floor |
| 1.3 | Admin console tests | `make test-frontend` | `# fail 0` |
| 1.4 | Integration suite | `make up && make test-integration` | all pass |
| 1.5 | Race detector | `make test-race` | no data races |
| 1.6 | Docs consistent | `make check-docs` | 0 broken links, all metrics match |
| 1.7 | CI green on the release commit | GitHub Actions | 4 jobs + CodeQL |

> 1.5 is **not** part of `make ci` — the race detector is slow enough that it
> would tax every PR. Releases are the right cadence for it.

---

## Phase 2 — Documentation

| # | Gate | Where |
|---|---|---|
| 2.1 | `CHANGELOG.md` `[Unreleased]` matches the real diff | `git log <prev-tag>..HEAD --oneline` |
| 2.2 | `[Unreleased]` renamed to `[X.Y.Z] — YYYY-MM-DD`, new empty `[Unreleased]` added | [CHANGELOG.md](../CHANGELOG.md) |
| 2.3 | Breaking changes have a `### Breaking` subsection with a migration note | CHANGELOG |
| 2.4 | `PROJECT_STATUS.md` header updated: version, date, maturity | [PROJECT_STATUS.md](PROJECT_STATUS.md#current-state) |
| 2.5 | Metrics re-derived, not copied | `make check-metrics` |
| 2.6 | `FEATURES.md` reflects what actually shipped | [FEATURES.md](FEATURES.md) |
| 2.7 | Shipped roadmap items moved to the delivered table | [ROADMAP.md](ROADMAP.md) |
| 2.8 | Resolved debt marked **Resolved** with a date (entry kept, not deleted) | [TECH_DEBT.md](TECH_DEBT.md) |
| 2.9 | Fixed defects marked **Fixed** with their regression guard named | [KNOWN_ISSUES.md](KNOWN_ISSUES.md) |
| 2.10 | `README.md` feature table still accurate | [README.md](../README.md) |
| 2.11 | New/changed config documented in MODULES, `.env.example`, **and `docker-compose.yml`** | [TD-004](TECH_DEBT.md#td-004) is the standing reminder |

> 2.8 and 2.9 say *keep the entry*. The record of why a shortcut existed is
> worth more than a tidy list, and a deleted issue is an issue that can return
> unnoticed.

---

## Phase 3 — Manual validation against a live stack

Automated gates cannot cover this yet — there is no end-to-end suite
([V1-01](ROADMAP.md#v1-01--automated-end-to-end-test-suite)). Until there is,
this phase is done by hand and is the reason a release takes an afternoon
rather than a minute.

```bash
make reset-dev     # clean stack from scratch — proves a first-time install works
make e2e           # readiness + auth smoke test
```

| # | Check | Expect |
|---|---|---|
| 3.1 | Clean-slate bring-up succeeds | All 5 containers healthy |
| 3.2 | `make auth-test` | 200 |
| 3.3 | Admin console loads, PKCE login works | User card shows the signed-in admin, not "not signed in" |
| 3.4 | Users / Roles / Sessions / Invitations views load and paginate | No console errors |
| 3.5 | One create, one update, one delete through the UI | Succeeds and appears in Audit Logs |
| 3.6 | Audit Logs view shows the mutations from 3.5 | Correct actor, action, target |
| 3.7 | Invitation email arrives | Mailpit at `http://localhost:8025` |
| 3.8 | Non-admin token is rejected on `/admin/*` | 403 |
| 3.9 | Swagger UI renders and matches the shipped routes | `/swagger/index.html` |
| 3.10 | Production flag posture | `ADMIN_CONSOLE_ENABLED=true` + `DEV_PLAYGROUND_ENABLED=false` boots, console works, `/dev/auth` is 404 |

> 3.3 and 3.10 exist because of [KI-001](KNOWN_ISSUES.md#ki-001): the console
> was broken in **both** flag configurations for six weeks — a crash in one, a
> silent UI regression in the other — and nothing automated noticed. Check both
> until an e2e suite does it for you.

### Security probes

```bash
./scripts/security_live_check.sh       # public / protected / role-gated surfaces
./scripts/security_gap1_check.sh       # live-admin revocation (GAP-1)
./scripts/security_advanced_check.sh   # rate limiting, replay, escalation
```

| # | Check | Expect |
|---|---|---|
| 3.11 | All three scripts pass | No new FAIL versus the previous release |
| 3.12 | New findings triaged | Recorded in [KNOWN_ISSUES.md](KNOWN_ISSUES.md) with a severity, or fixed |

---

## Phase 4 — Release decision

| # | Gate | Rule |
|---|---|---|
| 4.1 | Zero **Critical** open issues | A Critical entry blocks the release. No exceptions |
| 4.2 | Every **High** issue has an explicit ship/slip decision | Written into the CHANGELOG, not just decided in someone's head |
| 4.3 | Known regressions from the previous release: none | Compare [KNOWN_ISSUES.md](KNOWN_ISSUES.md) against the previous tag |
| 4.4 | Rollback path known | Previous tag builds and runs; any schema change is reversible |

**Go / No-go.** Any 4.x failing is a No-go. Fix, or drop the offending change
from the release.

---

## Phase 5 — Tag and publish

```bash
# 1. Commit the documentation updates from Phase 2
git add CHANGELOG.md docs/ README.md
git commit -m "docs(release): prepare vX.Y.Z"

# 2. Annotated tag — the message becomes the release note
git tag -a vX.Y.Z -m "vX.Y.Z — <one-line theme>"

# 3. Push commit and tag
git push origin main
git push origin vX.Y.Z
```

| # | Gate | Notes |
|---|---|---|
| 5.1 | Tag is **annotated**, not lightweight | `git tag -a` — lightweight tags carry no message or author |
| 5.2 | Tag name matches the CHANGELOG heading exactly | `v0.4.0` ↔ `## [0.4.0]` |
| 5.3 | CI green on the tagged commit | Re-check after pushing |
| 5.4 | GitHub Release created from the CHANGELOG section | — |
| 5.5 | Binary artifact attached if relevant | CI job `gate` uploads `bin/api` |

> **Every tag needs a CHANGELOG entry.** `v0.3.1` shipped without one
> ([KI-008](KNOWN_ISSUES.md#ki-008)) and had to be reconstructed from git
> history two months later. 5.2 exists to prevent a repeat.

---

## Phase 6 — Post-release

| # | Action | Why |
|---|---|---|
| 6.1 | Add a fresh empty `[Unreleased]` to the CHANGELOG | The next PR has somewhere to write |
| 6.2 | Update the version line in [PROJECT_STATUS.md](PROJECT_STATUS.md) | It states the last tag and the unreleased commit count |
| 6.3 | Close the milestone; roll unfinished items forward with a reason | Silent disappearance hides a decision |
| 6.4 | Consider raising the coverage floor | Only if coverage genuinely moved up and held |
| 6.5 | Consider promoting a deferred linter | See the ratchet in [QUALITY_GATE.md](QUALITY_GATE.md#the-lint-ratchet--read-before-adding-a-linter) |
| 6.6 | Record anything the gates failed to catch | A missed defect is a gate defect — fix both |

---

## Hotfix path

For a Critical production defect, skip Phases 0 and 3 partially — but never
Phase 1.

```bash
git checkout -b hotfix/vX.Y.Z+1 vX.Y.Z
# minimal fix + regression test
make ci && make coverage-gate
```

| # | Gate |
|---|---|
| H1 | The fix is minimal — no refactoring rides along |
| H2 | A regression test exists that **fails without the fix** |
| H3 | Phase 1 gates all pass |
| H4 | Phase 3 reduced to the affected surface plus `make auth-test` |
| H5 | CHANGELOG gets a `[X.Y.Z+1]` patch entry |
| H6 | The defect is recorded in [KNOWN_ISSUES.md](KNOWN_ISSUES.md) as **Fixed**, with its regression guard named |
| H7 | Merged back into `main` |

---

## Release history

| Version | Date | Theme |
|---|---|---|
| `v0.3.1` | 2026-05-25 | Landing page (CHANGELOG entry added retroactively 2026-07-26) |
| `v0.3.0` | 2026-05-25 | Production hardening — rate limiting, audit buffer, CI, CodeQL |
| `v0.2.0` | — | Identity management CRUD |
| `v0.1.0-auth-foundation` | — | Authentication foundation |

Next planned: **v0.4.0** — see [MILESTONE_v0.4.md](MILESTONE_v0.4.md).
