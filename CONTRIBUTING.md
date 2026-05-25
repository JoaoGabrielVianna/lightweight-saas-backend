# Contributing

Thanks for taking the time to contribute! This project aims to be a reusable IAM foundation for SaaS products, so external contributions — bug reports, docs, fixes, features — are very welcome.

## Code of conduct

By participating, you agree to uphold our [Code of Conduct](CODE_OF_CONDUCT.md). Be kind, assume good faith, and keep discussion focused on the project.

## Ways to contribute

- **Found a bug?** → Open a [Bug report](.github/ISSUE_TEMPLATE/bug_report.yml).
- **Have an idea?** → Open a [Feature request](.github/ISSUE_TEMPLATE/feature_request.yml).
- **Security issue?** → Do **not** open a public issue. See [SECURITY.md](SECURITY.md).
- **Improving docs?** → PRs to anything under `docs/` are always welcome.

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

## Pull request expectations

A good PR:

- Targets `main`.
- Has a clear title and description (the PR template helps).
- Passes `make ci` locally.
- Includes tests for new behavior.
- Updates `docs/` when behavior or operator-facing surface changes.
- Updates [`CHANGELOG.md`](CHANGELOG.md) under the `Unreleased` section for user-visible changes.
- Does not bundle unrelated changes.

## Reporting bugs

Before filing a bug, please:

1. Search [existing issues](https://github.com/JoaoGabrielVianna/lightweight-saas-backend/issues?q=is%3Aissue) to avoid duplicates.
2. Try `make doctor` to rule out a local toolchain issue.
3. Capture the smallest reproduction you can — version (`git rev-parse --short HEAD`), commands run, expected vs. actual output.

Then open a [Bug report](.github/ISSUE_TEMPLATE/bug_report.yml).

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE) that covers the rest of the project.
