# Release Checklist

This is the standing checklist used to take a release candidate to a final tag. It is reused across releases; release-specific status lives in the per-release RC reports (e.g. [docs/RC1_REPORT.md](RC1_REPORT.md)).

**Current target:** `v0.2.0` (RC1 status: [RC1_REPORT.md](RC1_REPORT.md) — **GO**).

Conventions:

- `[ ]` = pending · `[x]` = done · `[~]` = deliberately skipped (record why).
- Each gate names the **owner** and the **evidence** that proves it.
- Stop at the first FAIL and route back to the relevant agent owner.

---

## Phase 0 — Pre-RC sanity

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 0.1 | Working tree is on the release branch (`milestone/auth-v1` for v0.2). | maintainer | `git rev-parse --abbrev-ref HEAD` |
| 0.2 | All RC-blocking PRs merged; no unmerged scope claims to be in the release. | maintainer | `git log v<previous>..HEAD --oneline` |
| 0.3 | `CHANGELOG.md` `[Unreleased]` matches the actual diff against the prior tag. | release-prep | `git diff v<previous>..HEAD -- CHANGELOG.md` |
| 0.4 | `docs/RELEASE_<version>.md` exists and is up to date. | release-prep | Direct read |
| 0.5 | `config/project.json` validates against `config/project.schema.json`. | bootstrap | `jq -e .` + schema-validator step in `make ci` |

---

## Phase 1 — RC tag

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 1.1 | RC report `GO` and signed off. | release-prep | [RC1_REPORT.md §1](RC1_REPORT.md#1-decision) |
| 1.2 | All four agent reports landed under `docs/` with evidence under `docs/evidence/`. | release-prep | `ls docs/evidence/{api,screenshots,security,crud}` |
| 1.3 | `KNOWN_LIMITATIONS.md` has zero **Critical** entries; all **High** entries have an explicit "ship in 0.2.0 / slip to 0.2.x" decision. | release-prep | [KNOWN_LIMITATIONS.md §7](KNOWN_LIMITATIONS.md#7-summary-by-severity) |
| 1.4 | Tag `v<version>-rc<n>` against the validated commit. | maintainer | `git tag -a v0.2.0-rc1 -m "..."; git push --tags` |

---

## Phase 2 — Post-RC validation re-run

Run these against the *exact* RC build (i.e. the tagged commit, in a clean stack). Anything that previously passed must pass again; anything that previously gapped must explain itself in the same way (or better).

### 2.1 Stack bring-up

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 2.1.1 | `make doctor` exits 0. | maintainer | terminal log |
| 2.1.2 | `make purge && make up` produces a healthy 4-container stack within 2 minutes. | maintainer | `docker compose ps` |
| 2.1.3 | `make auth-test` returns 200 on `/me` for the seeded `testuser`. | maintainer | terminal log |

### 2.2 Security re-run (Agent A scope)

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 2.2.1 | `bash scripts/security_live_check.sh` exits 0 with **17/17 PASS**. | security | [docs/evidence/security/summary.txt](evidence/security/summary.txt) |
| 2.2.2 | `bash scripts/security_advanced_check.sh` exits 0 with **5 PASS / 0 FAIL** (INFO ≤ 6). | security | [docs/evidence/security/advanced/summary.txt](evidence/security/advanced/summary.txt) |
| 2.2.3 | No new findings have been added since [SECURITY_VALIDATION_v0.3.md §10](SECURITY_VALIDATION_v0.3.md#10-findings-carried-forward). If new ones appear, route them through [KNOWN_LIMITATIONS.md §1](KNOWN_LIMITATIONS.md#1-security--hardening-backlog) before continuing. | security | diff against prior summary.txt |

### 2.3 Browser E2E re-run (Agent B scope)

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 2.3.1 | Smoke pass exits with **PASS WITH GAPS**; every tab loads; tokens exchange via PKCE. | smoke | [SMOKE_TEST_v0.2.md §1](SMOKE_TEST_v0.2.md#1-result-summary), screenshots `01..09_*.png` |
| 2.3.2 | CRUD pass exits with **PASS WITH GAPS**; 0 FAIL; exactly 3 PASS_GAP (SMTP) and 1 NOT_IMPLEMENTED (realm-wide terminate-all) unless those have been resolved (then update this checklist). | smoke | [CRUD_E2E_REPORT.md §1](evidence/crud/CRUD_E2E_REPORT.md#1-result-matrix), `network/all.jsonl` |
| 2.3.3 | Browser console captured no JavaScript exceptions and no view-render crashes — only the documented SMTP `502` resource errors. | smoke | `docs/evidence/crud/console_log.txt` tail |

### 2.4 Invitation reliability spot-check (Agent C scope)

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 2.4.1 | `go test ./internal/identity/keycloak/... -run "TestCreateInvitation\|TestResendInvitation\|TestListInvitations"` is green. | identity | test runner output |
| 2.4.2 | Pagination stress test (`stress_test.go`) is green and reports the documented thresholds. | identity | test runner output |
| 2.4.3 | The 9 contract-changing tests enumerated in [INVITATION_RELIABILITY_v0.2.md §"Test coverage"](INVITATION_RELIABILITY_v0.2.md#test-coverage-for-the-new-contract) all exist and pass. | identity | grep + test runner |

### 2.5 Audit / observability decision (Agent D handoff)

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 2.5.1 | **DECISION REQUIRED** — Does final 0.2.0 ship the audit-wiring step (L4)? Answer must be recorded in `CHANGELOG.md`. | maintainer | `CHANGELOG.md` entry |
| 2.5.2 a *(if shipping L4)* | `logging.WireDefault()` is called from bootstrap. | identity | grep `WireDefault` in `cmd/api/main.go` or `internal/server/` |
| 2.5.2 b *(if shipping L4)* | Each of the 13 mutation handlers in `internal/identity/handler.go` calls `audit.Record(...)` post-success with the action listed in [AUDIT_EVENTS.md "Mapping of handler → action"](AUDIT_EVENTS.md#2-call-sites-in-internalidentityhandlergo). | identity | grep `audit.Record` per handler |
| 2.5.2 c *(if shipping L4)* | Agent B's CRUD pass re-run shows one `audit ` log line per mutation; payload `json.Unmarshal`s into `audit.Event`. | smoke + identity | `docker compose logs api | grep '^.*audit ' | wc -l` |
| 2.5.3 *(if slipping L4)* | [KNOWN_LIMITATIONS.md §3](KNOWN_LIMITATIONS.md#3-audit--observability-handoff-agent-d) explicitly states "deferred to 0.2.1" and `CHANGELOG.md` calls it out. | release-prep | direct read |

---

## Phase 3 — Build & docs gates

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 3.1 | `make ci` passes (`fmt-check + vet + build + test + swagger-check`). | maintainer | CI run log |
| 3.2 | `make swagger-check` shows no drift between handler annotations and `docs/swagger.{json,yaml,docs.go}`. | maintainer | `make swagger-check` exit 0 |
| 3.3 | `make test-race` is green. | maintainer | test runner output |
| 3.4 | `README.md` status banner, "What's in the box" bullets, API-surface table, and project-layout block all match the release. | release-prep | direct read against [README.md](../README.md) |
| 3.5 | `CHANGELOG.md` `[<version>]` entry has a real date (not "TBD"), accurate Added/Changed/Removed/Breaking subsections, and a compare link to the prior tag. | release-prep | direct read against [CHANGELOG.md](../CHANGELOG.md) |
| 3.6 | `docs/RELEASE_<version>.md` matches `CHANGELOG.md` and references the RC report. | release-prep | direct read |

---

## Phase 4 — Final tag

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 4.1 | RC build commit and final-tag commit are the same SHA (no last-minute "small fix" without a fresh RC). | maintainer | `git rev-parse v<version>-rc<n>` vs `HEAD` |
| 4.2 | Tag `v<version>` against the validated commit. **Annotated, signed, message references RC report.** | maintainer | `git tag -a v0.2.0 -m "..."` |
| 4.3 | `git push origin v<version>` succeeded. | maintainer | `git ls-remote --tags origin` |
| 4.4 | Release notes published (GitHub Releases, or equivalent) pointing to `docs/RELEASE_<version>.md`. | maintainer | release URL |
| 4.5 | `CHANGELOG.md` `[Unreleased]` reset to empty for the next cycle. | release-prep | direct read |

---

## Phase 5 — Post-release sanity

| # | Gate | Owner | Evidence |
|---|------|-------|----------|
| 5.1 | A fresh clone + `make up` + `make auth-test` produces a 200 on `/me` against the tag. | maintainer | terminal log on a clean checkout |
| 5.2 | At least one untrusted reader (someone who did not work on this release) can run the [Quickstart](../README.md#quickstart) end-to-end without back-channel help. | maintainer | written confirmation |
| 5.3 | All findings carried forward (F1–F3, L1–L10) have an issue / backlog ticket so they don't get lost. | release-prep | issue tracker links recorded in [KNOWN_LIMITATIONS.md](KNOWN_LIMITATIONS.md) |
| 5.4 | Coordinator decommissions any RC-specific scaffolding (e.g. `RC1_REPORT.md` stays as historical record; a fresh `RC<n>_REPORT.md` is created for the next cycle). | release-prep | repo state |

---

## Quick reference — re-run commands

```bash
# Security
bash scripts/security_live_check.sh
bash scripts/security_advanced_check.sh

# Browser smoke + CRUD (drivers live outside the repo by design)
node /tmp/smoketest_v02/smoke.spec.mjs
node /tmp/smoketest_v02/crud.spec.mjs

# Invitation reliability + audit unit tests
go test ./internal/identity/... ./internal/audit/... ./internal/logging/... -race

# CI gates
make ci
make swagger-check
make test-race
```

---

## Notes for future releases

- Keep this file release-agnostic. Anything specific to a single release belongs in the RC report or `CHANGELOG.md`, not here.
- If a phase consistently catches nothing, remove it — a checklist that always passes is a comment, not a gate.
- If a new validation surface is added (e.g. a load test, a fuzz pass), add a Phase 2 sub-section for it with named owner + evidence.
