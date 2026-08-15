# Contributing

Thanks for taking the time to contribute! This project aims to be a reusable IAM foundation for SaaS products, so external contributions — bug reports, docs, fixes, features — are very welcome.

## Code of conduct

Be kind, assume good faith, and keep discussion focused on the project.
Harassment of any kind is not tolerated. (There is no separate
`CODE_OF_CONDUCT.md` file yet — see [TD-017](docs/TECH_DEBT.md#td-017).)

## Ways to contribute

- **Found a bug?** → Open an [issue](https://github.com/JoaoGabrielVianna/lightweight-saas-backend/issues/new). Include the reproduction details listed under [Reporting bugs](#reporting-bugs).
- **Have an idea?** → Open an [issue](https://github.com/JoaoGabrielVianna/lightweight-saas-backend/issues/new) describing the problem before the solution.
- **Security issue?** → Do **not** open a public issue. See [SECURITY.md](SECURITY.md).
- **Improving docs?** → PRs to anything under `docs/` are always welcome. Read [docs/PROJECT_STATUS.md](docs/PROJECT_STATUS.md) first so you know what the canonical set is.

For non-trivial changes (new endpoints, architecture changes, dependency bumps), please open an issue first to discuss the approach before sending a PR.

## Local setup

```bash
git clone https://github.com/JoaoGabrielVianna/lightweight-saas-backend.git
cd lightweight-saas-backend
make doctor          # verify toolchain (Go 1.25+, Docker, free ports)
make init            # interactive bootstrap → config/project.json + .env
make up              # postgres + keycloak + api
make auth-test       # smoke test → expect 200
```

Full walkthrough in [`docs/getting-started/QUICKSTART.md`](docs/getting-started/QUICKSTART.md).

## Development workflow

1. Fork the repo and create a topic branch from `main`:
   ```bash
   git checkout -b feat/short-description
   ```
2. Make your changes. Keep commits focused; split unrelated work into separate PRs.
3. Run the CI gate locally **before pushing**:
   ```bash
   make ci
   ```
   This mirrors the gate that GitHub Actions will run:
   `fmt-check + vet + build + test + swagger-check`.
4. If you touched any Swagger handler annotation, regenerate the docs and commit them:
   ```bash
   make docs && git add docs/
   ```
   Otherwise `swagger-check` will fail in CI.
5. Open a pull request against `main`. Fill in the PR template.

## Coding standards

- **Formatting** — `gofmt` is enforced; `make fmt` will fix anything.
- **Static analysis** — `make vet` (and `make lint` if you have `golangci-lint` installed).
- **Tests** — add a test for any non-trivial change. Run with `make test` (or `make test-race` for the race detector). Integration tests live behind the `integration` build tag and require the stack to be up.
- **Commit messages** — [Conventional Commits](https://www.conventionalcommits.org/) preferred:
  - `feat:` new feature
  - `fix:` bug fix
  - `docs:` documentation only
  - `chore:` tooling, refactors, dependency bumps
  - `test:` test-only changes
- **No AI attribution in commit messages.** A commit records who is
  accountable for a change, and that is a person. Do not add `Co-Authored-By`
  trailers naming Claude, ChatGPT, Copilot or any other agent or vendor, and do
  not add `Generated with …`, `AI-assisted` or `AI-generated` footers. Human
  co-authors are welcome and unaffected — `Co-Authored-By: Jane Developer
  <jane@example.com>` is correct and stays. Enforced by the `commit-msg` hook
  and by the `commit attribution` CI job; check locally with
  `make check-attribution`. If you use a coding agent, [CLAUDE.md](CLAUDE.md)
  states the rule in the form the agent reads.

## Pull request expectations

A good PR:

- Targets `main`.
- Has a clear title and description.
- Passes `make ci` locally.
- Includes tests for new behavior.
- Updates [`CHANGELOG.md`](CHANGELOG.md) under the `Unreleased` section for user-visible changes.
- Does not bundle unrelated changes.

If the PR touches `sdk/go/`, two extra things apply:

- The SDK is a **separate module**, so `make ci` reaches it only through the
  `sdk-*` targets. `make sdk-check` is the one to run.
- Changing an exported declaration changes what consumers depend on. The gate
  will fail until you run `make sdk-api-update` and commit the resulting
  [`sdk/go/api.txt`](sdk/go/api.txt) diff. That is the review asking whether the
  break is intended — pre-v1 it may be, and it belongs under **Breaking** in
  [`sdk/go/CHANGELOG.md`](sdk/go/CHANGELOG.md).

### Before you open a PR

```bash
make hooks-install   # once per clone — commit-msg + pre-commit + pre-push checks
make ci              # what CI enforces  (~5s)
make ci-full         # + coverage floor + frontend tests  (~10s)
```

Two documents carry the standard:

- **[docs/CONTRIBUTION_CHECKLIST.md](docs/CONTRIBUTION_CHECKLIST.md)** — the
  tickable short form. Copy it into your PR description.
- **[docs/QUALITY_GATE.md](docs/QUALITY_GATE.md)** — the full criteria, what is
  automated, and what needs reviewer judgement.

Most of what used to be a review checklist is now enforced mechanically:
formatting, `go vet`, 9 linters, the coverage floor, OpenAPI drift, broken
documentation links, and documented numbers that no longer match the code. Put
review effort into the parts a machine cannot judge — security, performance,
and whether the prose is still true.

### Keeping the documentation honest

The canonical documentation set lives in [`docs/`](docs/) and is expected to
match the code exactly. Update whichever of these your change affects:

| If your change… | Update |
|---|---|
| Adds, removes or renames a route | [`docs/FEATURES.md`](docs/FEATURES.md) + the route counts in [`docs/PROJECT_STATUS.md`](docs/PROJECT_STATUS.md#metrics) |
| Adds an audit action | [`docs/FEATURES.md`](docs/FEATURES.md) + the action count in `PROJECT_STATUS.md` |
| Changes a module's scope or maturity | [`docs/MODULES.md`](docs/MODULES.md) |
| Changes layering, middleware order, or an invariant | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) |
| Makes an architectural decision | Add an `AD-nnn` entry to `PROJECT_STATUS.md` |
| Introduces a shortcut | Add a `TD-nnn` entry to [`docs/TECH_DEBT.md`](docs/TECH_DEBT.md) |
| Fixes or discovers a defect | [`docs/KNOWN_ISSUES.md`](docs/KNOWN_ISSUES.md) — record the regression guard too |
| Adds a config value | `docs/MODULES.md` env table, `.env.example`, **and `docker-compose.yml`** |
| Ships or reprioritizes roadmap work | [`docs/ROADMAP.md`](docs/ROADMAP.md) |

**Never copy a number between documents.** Re-derive it — the commands are in
[`docs/PROJECT_STATUS.md`](docs/PROJECT_STATUS.md#metrics). Stale counts are how
this repository accumulated two months of misleading documentation
([TD-001](docs/TECH_DEBT.md#td-001)).

## Reporting bugs

Before filing a bug, please:

1. Search [existing issues](https://github.com/JoaoGabrielVianna/lightweight-saas-backend/issues?q=is%3Aissue) to avoid duplicates.
2. Try `make doctor` to rule out a local toolchain issue.
3. Capture the smallest reproduction you can — version (`git rev-parse --short HEAD`), commands run, expected vs. actual output.

Then open an [issue](https://github.com/JoaoGabrielVianna/lightweight-saas-backend/issues/new).

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE) that covers the rest of the project.
