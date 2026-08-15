# Contribution Checklist

**Last updated:** 2026-07-27

Copy this into your PR description and tick as you go. Full rationale for
every item is in [QUALITY_GATE.md](QUALITY_GATE.md); this page is the short
form you actually use.

---

## Before you start

- [ ] I read [PROJECT_STATUS.md](PROJECT_STATUS.md) — I know what exists and what does not
- [ ] For anything non-trivial, I opened an issue first
- [ ] I ran `make hooks-install` (once per clone)

---

## The 60-second version

```bash
make ci          # what CI enforces  (~40s)
make ci-full     # + coverage + frontend tests  (~70s)
```

If both pass and you updated the docs your change affects, you are almost
certainly fine. The rest of this page is the detail.

---

## Code

- [ ] `make ci` passes locally
- [ ] `make lint` clean — no new `//nolint` without `// reason`
- [ ] New behaviour has tests; bug fixes have a test that **fails without the fix**
- [ ] `make coverage-gate` passes (floor 73%)
- [ ] Business rules live in the **service** tier, not the handler
- [ ] No new import from `internal/auth` → `internal/identity` (would create a cycle)
- [ ] `internal/audit` still imports nothing beyond stdlib

## API changes

- [ ] Handler carries Swagger annotations
- [ ] `make docs && git add docs/` — otherwise `swagger-check` fails
- [ ] Route added to the correct group (gated `/admin/*` vs. authenticated vs. public)
- [ ] Mutations call `logging.RecordMutation(...)` on **both** success and failure
- [ ] Route table in [FEATURES.md](FEATURES.md) updated

## Configuration changes

- [ ] Field added to `Config` and read in `LoadConfig`
- [ ] Added to `Validate()` if required at boot
- [ ] Documented in [MODULES.md](MODULES.md#internalconfig) env table
- [ ] Added to `.env.example`
- [ ] **Added to `docker-compose.yml`** ← the step that gets forgotten ([TD-004](TECH_DEBT.md#td-004))

## Database changes

- [ ] Rollback validated: apply → roll back → re-apply on a scratch database
- [ ] PR describes locking behaviour and expected duration
- [ ] Data model updated in [ARCHITECTURE.md §8](ARCHITECTURE.md#8-data-layer)
- [ ] Adding a **second table**? Stop — that should be blocked on [V1-07](ROADMAP.md#v1-07--versioned-migrations)

## Security

- [ ] No secret, token, or password in logs or response bodies
- [ ] New `/admin/*` routes are inside the gated group
- [ ] Caller-supplied identifiers validated before reaching a provider
- [ ] Pagination clamped
- [ ] `RequireLiveAdmin` still fails closed
- [ ] No `.env` staged (the pre-commit hook blocks it, but `git add -f` would not)

## Performance

- [ ] New list endpoints paginate; truncation is visible, never silent
- [ ] No N+1 — a per-item request loop is bounded and has an aggregate timeout
- [ ] Outbound calls carry a context deadline
- [ ] New caches document TTL, invalidation, and failure behaviour

## Documentation

- [ ] `make check-docs` passes (links + published numbers)
- [ ] [CHANGELOG.md](../CHANGELOG.md) `[Unreleased]` updated for user-visible changes
- [ ] Updated whichever apply: [FEATURES.md](FEATURES.md) · [MODULES.md](MODULES.md) · [ARCHITECTURE.md](ARCHITECTURE.md) · [PROJECT_STATUS.md](PROJECT_STATUS.md) · [ROADMAP.md](ROADMAP.md) · [README.md](../README.md)
- [ ] Introduced a shortcut? Recorded as `TD-nnn` in [TECH_DEBT.md](TECH_DEBT.md)
- [ ] Fixed a defect? Recorded in [KNOWN_ISSUES.md](KNOWN_ISSUES.md) **with the regression guard**
- [ ] I did not copy a number between documents — I re-derived it

## PR hygiene

- [ ] Targets `main`
- [ ] Conventional Commit title (`feat:` `fix:` `docs:` `chore:` `test:` `refactor:`)
- [ ] One concern per PR — unrelated changes split out
- [ ] Description says **why**, not only what
- [ ] Used `--no-verify`? Said so in the description and explained why

---

## The five mistakes that actually happen here

Ranked by how often they have occurred in this repository's history, not by
how bad they sound.

1. **Reimplementing something that exists instead of reusing it.**
   [KI-001](KNOWN_ISSUES.md#ki-001) shipped a second, degraded `/auth/debug`
   handler beside a working one. It panicked the process at boot for six weeks.
   Search before you write.

2. **Adding a config value without wiring it into `docker-compose.yml`.**
   Four variables are currently unreachable in the only supported deployment
   artifact ([TD-004](TECH_DEBT.md#td-004)), including a whole CORS feature.

3. **Changing a widely-used signature without updating the tests.**
   Commit `e2a3bcd` exists solely to repair this. `SetupRouter` takes eight
   positional parameters ([TD-006](TECH_DEBT.md#td-006)) — treat any change to
   it as a breaking change.

4. **Letting a documented number drift.** The README once claimed 22 routes
   against an actual 46. `make check-metrics` now catches this, but only for
   the numbers it knows about — add yours to
   [`scripts/check_doc_metrics.py`](../scripts/check_doc_metrics.py).

5. **Measuring coverage without `-count=1`.** Go's test cache reports stale
   per-package results. This produced a phantom 5-point regression during the
   2026-07-26 audit. `make coverage-gate` always passes the flag; ad-hoc
   `go test -cover` runs do not.

---

## Getting help

| Question | Where |
|---|---|
| What already exists? | [FEATURES.md](FEATURES.md) — every claim has a code reference |
| How is this built? | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Where does my change belong? | [ARCHITECTURE.md §13](ARCHITECTURE.md#13-extension-guide) |
| Is this a known problem? | [KNOWN_ISSUES.md](KNOWN_ISSUES.md) · [TECH_DEBT.md](TECH_DEBT.md) |
| Is this planned already? | [ROADMAP.md](ROADMAP.md) |
| Is the project healthy right now? | [HEALTH_CHECK.md](HEALTH_CHECK.md) |
| Why did a gate fail? | [QUALITY_GATE.md](QUALITY_GATE.md) |
