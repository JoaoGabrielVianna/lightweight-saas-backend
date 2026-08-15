# =============================================================================
# Lightweight SaaS Backend — Developer Makefile
#
# Convention: every target carries a `## <category>: description` comment.
# `make help` parses those to produce a categorized, colored help screen.
#
# Compatibility: GNU Make 3.81+ (ships with macOS) and BSD/Linux make.
# Uses POSIX shell only — no bash-isms.
# =============================================================================

SHELL := /bin/sh
.SHELLFLAGS := -eu -c
.DEFAULT_GOAL := help

# --- tooling probes ---------------------------------------------------------
GO        := go
DOCKER    := docker
COMPOSE   := $(shell command -v docker-compose 2>/dev/null || echo "docker compose")

# --- paths ------------------------------------------------------------------
BIN_DIR        := bin
API_BINARY     := $(BIN_DIR)/api
CONFIG_JSON    := config/project.json
KEYCLOAK_DIR   := deploy/keycloak

# =============================================================================
# Help
# =============================================================================

.PHONY: help
help: ## meta: Show this help screen
	@printf "\n\033[1mLightweight SaaS Backend — make targets\033[0m\n\n"
	@awk 'BEGIN {FS = ":.*?## "} \
		/^[a-zA-Z_-]+:.*?## / { \
			split($$2, a, ": "); cat=a[1]; desc=a[2]; \
			if (cat != prev) { printf "\n  \033[1;33m%s\033[0m\n", cat; prev=cat } \
			printf "    \033[36m%-22s\033[0m %s\n", $$1, desc \
		}' $(MAKEFILE_LIST)
	@printf "\n"

# =============================================================================
# Setup / Diagnostics
# =============================================================================

.PHONY: setup
setup: ## setup: Install Go modules and copy .env from .env.example if absent
	@$(MAKE) -s doctor
	@test -f .env || (cp .env.example .env && echo "  + created .env from .env.example")
	@$(GO) mod download
	@echo "  + go modules downloaded"

.PHONY: doctor
doctor: ## setup: Diagnose toolchain, docker daemon, stack state, and port conflicts
	@printf "\n\033[1m── required tools ─────────────────────────────────\033[0m\n"
	@command -v $(GO)     >/dev/null && printf "  + %-9s %s\n" "go"      "$$($(GO) version)"      || { echo "  - go: MISSING";     exit 1; }
	@command -v $(DOCKER) >/dev/null && printf "  + %-9s %s\n" "docker"  "$$($(DOCKER) --version)" || { echo "  - docker: MISSING"; exit 1; }
	@$(COMPOSE) version >/dev/null 2>&1 && printf "  + %-9s %s\n" "compose" "$$($(COMPOSE) version 2>&1 | head -1)" || { echo "  - docker-compose: MISSING"; exit 1; }
	@command -v curl >/dev/null && printf "  + %-9s present\n" "curl" || printf "  - %-9s MISSING (auth-test/e2e will fail)\n" "curl"
	@command -v jq   >/dev/null && printf "  + %-9s present\n" "jq"   || printf "  - %-9s MISSING (auth-test/e2e will fail)\n" "jq"
	@printf "\n\033[1m── docker daemon ──────────────────────────────────\033[0m\n"
	@if $(DOCKER) info >/dev/null 2>&1; then \
		$(DOCKER) info --format '  + server={{.ServerVersion}}  containers={{.Containers}} (running={{.ContainersRunning}})  images={{.Images}}'; \
	else \
		echo "  - docker daemon NOT REACHABLE (is Docker Desktop running?)"; exit 1; \
	fi
	@printf "\n\033[1m── stack containers ───────────────────────────────\033[0m\n"
	@out=$$($(COMPOSE) ps --format 'table {{.Name}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null); \
	if [ -z "$$out" ] || [ "$$(echo "$$out" | wc -l)" -le 1 ]; then \
		echo "  i no project containers running (try 'make up')"; \
	else \
		echo "$$out" | sed 's/^/  /'; \
	fi
	@printf "\n\033[1m── ports of interest ──────────────────────────────\033[0m\n"
	@for port in 8080 8081 5432 5433; do \
		holder=$$(lsof -nP -iTCP:$$port -sTCP:LISTEN 2>/dev/null | tail -n +2 | head -1 | awk '{print $$1 " (pid " $$2 ")"}'); \
		if [ -z "$$holder" ]; then \
			printf "  + %-5s free\n" "$$port"; \
		else \
			printf "  ! %-5s in use by %s\n" "$$port" "$$holder"; \
		fi; \
	done
	@printf "\n\033[1m── api reachability ───────────────────────────────\033[0m\n"
	@if curl -fsS -o /dev/null --max-time 2 http://localhost:8080/health 2>/dev/null; then \
		echo "  + /health responds 200"; \
	else \
		echo "  i /health unreachable (api not running, or listening elsewhere)"; \
	fi
	@if curl -fsS -o /dev/null --max-time 2 http://localhost:8081/realms/master/.well-known/openid-configuration 2>/dev/null; then \
		echo "  + keycloak OIDC discovery responds 200"; \
	else \
		echo "  i keycloak unreachable on :8081"; \
	fi
	@printf "\n"

# =============================================================================
# Build / Quality
# =============================================================================

.PHONY: build
build: ## build: Compile the API binary to bin/api
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(API_BINARY) ./cmd/api
	@echo "  + built $(API_BINARY)"

.PHONY: test
test: ## quality: Run all Go tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## quality: Run tests with the race detector (slower)
	$(GO) test -race ./...

# -p 1 is REQUIRED here, and is not caution.
#
# Go runs package test binaries in parallel. Under -race each one is several
# times slower while holding its database connections just as long, and the
# integration suites all point at ONE PostgreSQL. At default parallelism they
# exhaust it and fail with errors that read like product bugs — the concurrency
# test in internal/identityruntime reports `internal_error` from a resolver that
# is working perfectly. Serialising the packages costs about a minute and makes
# the run mean what it says.
.PHONY: test-race-integration
test-race-integration: ## quality: Race detector over the integration suite too. Requires DB_URL (+ KEYCLOAK_VERIFY_URL for the realm suites).
	@if [ -z "$${DB_URL:-}" ]; then \
		echo "  ✗ DB_URL unset — the integration suites would skip and the run would prove nothing"; \
		exit 1; \
	fi
	$(GO) test -race -p 1 -count=1 -tags=integration ./...

.PHONY: test-cover
test-cover: ## quality: Run tests with coverage; writes coverage.out and prints summary
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	@$(GO) tool cover -func=coverage.out | tail -1

.PHONY: test-integration
test-integration: ## quality: Run integration tests (build tag: integration). Requires the stack to be up.
	$(GO) test -tags=integration ./...

.PHONY: vet
vet: ## quality: Run go vet across the module
	$(GO) vet ./...

.PHONY: fmt
fmt: ## quality: Apply gofmt to all Go files (mutates source)
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## quality: Fail if any Go file would be reformatted (no mutation)
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "  - the following files need formatting:"; \
		echo "$$unformatted" | sed 's/^/    /'; \
		exit 1; \
	fi

# golangci-lint version is pinned so local runs and CI agree. A floating
# @latest means a new release can turn a green branch red with no code change.
GOLANGCI_VERSION ?= v2.12.2
GOLANGCI := $(shell command -v golangci-lint 2>/dev/null || echo "$(shell go env GOPATH)/bin/golangci-lint")

.PHONY: lint-install
lint-install: ## quality: Install the pinned golangci-lint into $$GOPATH/bin
	@echo "  installing golangci-lint $(GOLANGCI_VERSION)…"
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@echo "  + $(GOLANGCI)"

.PHONY: lint
lint: ## quality: Run golangci-lint (config: .golangci.yml). BLOCKING in CI.
	@if [ ! -x "$(GOLANGCI)" ] && ! command -v golangci-lint >/dev/null; then \
		echo "  ✗ golangci-lint not installed — run 'make lint-install'"; \
		exit 1; \
	fi
	@$(GOLANGCI) run ./...

.PHONY: test-frontend
test-frontend: ## quality: Run the admin console's node --test suites
	@if ! command -v node >/dev/null; then \
		echo "  ✗ node not installed — required for the admin console tests"; \
		exit 1; \
	fi
	@node --test web/admin/static/js/tests/ 2>&1 | tail -12

.PHONY: coverage-gate
coverage-gate: ## quality: Fail if coverage drops below the floor (unit, or full when DB_URL is set)
	@./scripts/check_coverage.sh

.PHONY: coverage-gate-unit
coverage-gate-unit: ## quality: Coverage floor, untagged only — the fast local gate, no database needed
	@COVERAGE_MODE=unit ./scripts/check_coverage.sh

.PHONY: coverage-gate-full
coverage-gate-full: ## quality: AUTHORITATIVE coverage floor — runs the integration suite too. Requires DB_URL.
	@COVERAGE_MODE=full ./scripts/check_coverage.sh

# ─── The Go SDK (sdk/go) ────────────────────────────────────────────────────
#
# The SDK is a SEPARATE Go module, so `go test ./...`, `go vet ./...` and
# `golangci-lint run ./...` at the root do not see it — module boundaries stop
# the `./...` walk. Only `gofmt -l .` reaches it, because gofmt walks
# directories rather than modules.
#
# That is not an accident to be worked around. A separate module is what proves
# the SDK could be extracted and what stops server packages leaking into a public
# client API. The cost is that its gates have to be named explicitly, here, and
# `ci` has to invoke them — which is exactly what would otherwise be forgotten.
SDK_DIR := sdk/go

# The floor is SEPARATE from the server's on purpose.
#
# Folding SDK statements into the aggregate would move a number nobody agreed to
# move: a well-tested transport layer would raise the server's apparent coverage,
# and a future untested SDK addition would lower it, in both cases reporting
# something about the wrong codebase. Two floors, two numbers, no interference.
#
# 88 rather than 100. What remains uncovered is the build-info version lookup,
# which needs a build in which this module is a DEPENDENCY — impossible to
# arrange from its own test binary — and a handful of error branches on
# io.ReadAll. Chasing those with mocks would test the mocks.
SDK_COVERAGE_FLOOR ?= 88

.PHONY: sdk-test
sdk-test: ## sdk: Run the Go SDK's unit and contract suites
	@cd $(SDK_DIR) && $(GO) test -count=1 ./...

.PHONY: sdk-test-race
sdk-test-race: ## sdk: Run the SDK suite under the race detector (concurrent-client tests)
	@cd $(SDK_DIR) && $(GO) test -race -count=1 ./...

.PHONY: sdk-vet
sdk-vet: ## sdk: go vet inside the SDK module, including the acceptance build tag
	@cd $(SDK_DIR) && $(GO) vet ./...
	@cd $(SDK_DIR) && $(GO) vet -tags acceptance ./...

# Two checks, because either one alone has a blind spot.
#
# `go mod edit -json` reads the DECLARATION and cannot fail to resolve, which
# matters more than it sounds: `go list -m all` exits non-zero when a require
# cannot be downloaded, printing nothing to stdout — so the original form of this
# gate read a go.mod with an unresolvable dependency in it as "no dependencies".
# That is the offline case, which is to say the CI cache-miss case.
#
# `go list -m all` then covers what the declaration cannot: a dependency arriving
# transitively rather than through this go.mod.
.PHONY: sdk-deps-check
sdk-deps-check: ## sdk: Fail if the SDK has acquired any module dependency
	@cd $(SDK_DIR) && declared=$$($(GO) mod edit -json | python3 -c \
		'import json,sys; print("\n".join(r["Path"]+" "+r["Version"] for r in (json.load(sys.stdin).get("Require") or [])))'); \
	if [ -n "$$declared" ]; then \
		echo "  ✗ $(SDK_DIR)/go.mod declares dependencies:"; \
		echo "$$declared" | sed 's/^/      /'; \
		echo "    A dependency here becomes a dependency of every backend that imports"; \
		echo "    the SDK, together with its transitive graph and its advisories."; \
		exit 1; \
	fi
	@cd $(SDK_DIR) && if ! resolved=$$($(GO) list -m all 2>&1); then \
		echo "  ✗ go list -m all failed inside $(SDK_DIR):"; \
		echo "$$resolved" | sed 's/^/      /'; \
		exit 1; \
	elif echo "$$resolved" | tail -n +2 | grep -q .; then \
		echo "  ✗ the SDK has acquired dependencies:"; \
		echo "$$resolved" | tail -n +2 | sed 's/^/      /'; \
		exit 1; \
	fi
	@echo "  + SDK has no module dependencies"

# Coverage is measured over the LIBRARY package only, not `./...`.
#
# cmd/example is a program, not library code: its value is that it compiles and
# reads well, and it is built by `sdk-test`. Counting its statements would let
# the headline number be moved by adding or removing example lines, which is
# exactly the kind of movement that makes a coverage figure stop meaning
# anything. The acceptance suite is behind a build tag and does not appear here
# either — it runs against a real stack, and its evidence is its own.
SDK_COVER_PKG := .

.PHONY: sdk-coverage
sdk-coverage: ## sdk: Coverage report for the SDK library, reported separately from the server's
	@cd $(SDK_DIR) && $(GO) test -count=1 -coverprofile=coverage.out -covermode=atomic $(SDK_COVER_PKG) >/dev/null
	@cd $(SDK_DIR) && $(GO) tool cover -func=coverage.out | tail -1

.PHONY: sdk-coverage-gate
sdk-coverage-gate: ## sdk: Fail if SDK library coverage drops below $$SDK_COVERAGE_FLOOR
	@cd $(SDK_DIR) && $(GO) test -count=1 -coverprofile=coverage.out -covermode=atomic $(SDK_COVER_PKG) >/dev/null
	@cd $(SDK_DIR) && total=$$($(GO) tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	floor=$(SDK_COVERAGE_FLOOR); \
	awk -v t="$$total" -v f="$$floor" 'BEGIN { exit !(t+0 >= f+0) }' || { \
		echo "  ✗ SDK coverage $$total% is below the $$floor% floor"; exit 1; }; \
	echo "  + SDK coverage $$total% (floor $$floor%)"

# ─── The SDK's public API, as a reviewable file ─────────────────────────────
#
# sdk/go/api.txt records every exported declaration. Its whole purpose is to make
# a breaking change VISIBLE: `go test` cannot see one, because the SDK's own
# tests are updated in the same commit that breaks the API, so they pass while
# consumers break.
#
# Pre-v1 a breaking change is allowed. Making one without noticing is not.
.PHONY: sdk-api
sdk-api: ## sdk: Print the SDK's exported API (the snapshot format)
	@$(GO) run scripts/sdk-api-snapshot.go $(SDK_DIR)

.PHONY: sdk-api-update
sdk-api-update: ## sdk: Re-record sdk/go/api.txt after an INTENTIONAL API change
	@$(GO) run scripts/sdk-api-snapshot.go $(SDK_DIR) > $(SDK_DIR)/api.txt
	@echo "  + $(SDK_DIR)/api.txt updated — review the diff before committing"

.PHONY: sdk-api-check
sdk-api-check: ## sdk: Fail if the exported API drifted from sdk/go/api.txt
	@$(GO) run scripts/sdk-api-snapshot.go $(SDK_DIR) > $(SDK_DIR)/api.txt.actual
	@if ! diff -u $(SDK_DIR)/api.txt $(SDK_DIR)/api.txt.actual > $(SDK_DIR)/api.diff; then \
		echo "  ✗ the SDK's exported API changed and $(SDK_DIR)/api.txt was not updated"; \
		grep -E '^[-+][^-+]' $(SDK_DIR)/api.diff | head -25 | sed 's/^/      /'; \
		echo "    -  removed or changed — BREAKING for anyone already using it"; \
		echo "    +  added — a minor release"; \
		echo "    If the change is intended: make sdk-api-update"; \
		rm -f $(SDK_DIR)/api.txt.actual $(SDK_DIR)/api.diff; \
		exit 1; \
	fi
	@rm -f $(SDK_DIR)/api.txt.actual $(SDK_DIR)/api.diff
	@echo "  + SDK exported API matches $(SDK_DIR)/api.txt"

# The external-consumer boundary: a module OUTSIDE this repository compiles
# against the SDK. See the script's header for why this is not the same claim as
# the tag-resolution simulation below.
.PHONY: sdk-consumer-check
sdk-consumer-check: ## sdk: Prove an external module compiles against the SDK alone
	@./scripts/sdk-consumer-check.sh

# The drift gate. The module path, the tag prefix and the `go get` line in three
# READMEs are one fact expressed four times, and they drift by ordinary editing
# rather than by anything dramatic. Catching that only when a tag is pushed would
# be catching it after the wrong command is already the published one — so it is
# cheap on purpose (no tests, no toolchain, no git reads) and runs on every push.
.PHONY: sdk-identity-check
sdk-identity-check: ## sdk: Module path, tag prefix and documented install command must agree
	@./scripts/check-sdk-release.sh --identity >/dev/null || \
		{ ./scripts/check-sdk-release.sh --identity; exit 1; }
	@echo "  + SDK module path, tag prefix and install docs agree"

.PHONY: sdk-check
sdk-check: ## sdk: Every SDK gate that needs no database — identity, vet, deps, api, tests, coverage
	@$(MAKE) -s sdk-identity-check
	@$(MAKE) -s sdk-vet
	@$(MAKE) -s sdk-deps-check
	@$(MAKE) -s sdk-api-check
	@$(MAKE) -s sdk-test
	@$(MAKE) -s sdk-coverage-gate
	@$(MAKE) -s sdk-consumer-check

# ─── Release ────────────────────────────────────────────────────────────────
#
# Nothing here tags, pushes, or contacts a remote. These answer questions; the
# decision to publish stays a separately-typed act, because a release target one
# typo away from publishing eventually publishes a typo.
#
# The two dry-run modes are not redundant. A git tag captures a COMMIT, so
# `sdk-release-check` reads what a tag on HEAD would actually contain, while
# `sdk-release-dev` reads the files on disk. Today those differ completely — the
# SDK is untracked — and reporting the working tree's health as "ready to tag"
# would be a precise lie.
RELEASE_VERSION_GUARD = \
	[ -n "$(VERSION)" ] || { \
		echo "  - VERSION is required, e.g.  make $@ VERSION=v0.1.0"; \
		echo "    The SDK's first release is recommended to be v0.1.0; see docs/SDK_GO.md."; \
		exit 1; }

.PHONY: sdk-release-check
sdk-release-check: ## release: Would a tag on HEAD be a valid SDK release? Requires VERSION=vX.Y.Z
	@$(RELEASE_VERSION_GUARD)
	@./scripts/check-sdk-release.sh --head $(VERSION)

.PHONY: sdk-release-dev
sdk-release-dev: ## release: Same gates against the WORKING TREE (preconditions reported, not enforced)
	@$(RELEASE_VERSION_GUARD)
	@./scripts/check-sdk-release.sh --worktree $(VERSION)

.PHONY: sdk-release-verify-tag
sdk-release-verify-tag: ## release: Validate an existing SDK tag. Requires TAG=sdk/go/vX.Y.Z
	@[ -n "$(TAG)" ] || { echo "  - TAG is required, e.g. make $@ TAG=sdk/go/v0.1.0"; exit 1; }
	@./scripts/check-sdk-release.sh --tag $(TAG)

.PHONY: sdk-release-simulate
sdk-release-simulate: ## release: Publish to a throwaway git remote and consume it externally
	@./scripts/sdk-release-simulation.sh $(or $(VERSION),v0.1.0)

.PHONY: sdk-release-mutation-check
sdk-release-mutation-check: ## release: Prove the release gates fail when release mechanics are broken
	@./scripts/sdk-release-mutation-check.sh

.PHONY: sdk-publish-smoke
sdk-publish-smoke: ## release: AFTER pushing a tag — is it publicly installable? Requires VERSION=vX.Y.Z
	@$(RELEASE_VERSION_GUARD)
	@./scripts/first-publish-smoke.sh $(VERSION)

.PHONY: sdk-mutation-check
sdk-mutation-check: ## sdk: Prove the SDK's tests fail when the behaviour they pin is broken
	@./scripts/sdk-mutation-check.sh

.PHONY: authz-mutation-check
authz-mutation-check: ## quality: Prove the negative authorization matrix fails when the boundary is broken
	@./scripts/authz-mutation-check.sh

.PHONY: audit-mutation-check
audit-mutation-check: ## quality: Prove the transactional-audit suite fails when atomicity is broken. Requires DB_URL (throwaway).
	@[ -n "$${DB_URL:-}" ] || { \
		echo "  - DB_URL is required, and must point at a THROWAWAY database."; \
		echo "    Mutations 1-6 are about what PostgreSQL does on ROLLBACK; without one"; \
		echo "    the suite that catches them skips and every mutation reads as caught."; \
		echo "    DB_URL=postgres://saas:saas@localhost:5432/saas_mut?sslmode=disable make audit-mutation-check"; \
		exit 1; }
	@./scripts/audit-mutation-check.sh

.PHONY: product-acceptance
product-acceptance: ## e2e: Clone to credential to SDK request, two workspaces, two realms. Needs Docker + free ports.
	@echo "  i installs from .env.example into a disposable compose project;"
	@echo "    override LW_PORT / KC_PORT / PG_PORT if 18080 / 18081 / 15432 are taken."
	@./scripts/product-acceptance.sh

.PHONY: sdk-acceptance
sdk-acceptance: ## e2e: The SDK against a real API + PostgreSQL + provider. Requires DB_URL (throwaway).
	@if [ -z "$${DB_URL:-}" ]; then \
		echo "  ✗ DB_URL unset — point it at a THROWAWAY database"; \
		exit 1; \
	fi
	@./scripts/sdk-acceptance.sh

.PHONY: check-links
check-links: ## docs: Fail if any Markdown link or anchor is broken
	@python3 scripts/check_docs_links.py

.PHONY: check-metrics
check-metrics: ## docs: Fail if a number published in the docs disagrees with the code
	@python3 scripts/check_doc_metrics.py --quiet

.PHONY: check-docs
check-docs: ## docs: All documentation gates — links + published metrics
	@$(MAKE) -s check-links
	@$(MAKE) -s check-metrics

# RANGE defaults to the commits this branch adds on top of origin/main, so the
# target judges only what you wrote. Override it to audit something else:
#   make check-attribution RANGE=v0.3.1..HEAD
.PHONY: check-attribution
check-attribution: ## quality: Fail if a new commit message carries AI attribution
	@./scripts/check-commit-attribution.sh --self-test
	@if [ -n "$(RANGE)" ]; then \
		./scripts/check-commit-attribution.sh --range "$(RANGE)" && echo "  + no AI attribution in $(RANGE)"; \
	elif git rev-parse --verify --quiet origin/main >/dev/null; then \
		./scripts/check-commit-attribution.sh --range origin/main..HEAD && echo "  + no AI attribution in origin/main..HEAD"; \
	else \
		echo "  ! origin/main unavailable — pass RANGE=<base>..HEAD to check a range"; \
	fi

.PHONY: ci
ci: ## quality: The CI gate — fmt-check · vet · lint · build · test · sdk · swagger · docs
	@$(MAKE) -s fmt-check
	@$(MAKE) -s vet
	@$(MAKE) -s lint
	@$(MAKE) -s build
	@$(MAKE) -s test
	@$(MAKE) -s sdk-check
	@$(MAKE) -s swagger-check
	@$(MAKE) -s check-docs
	@$(MAKE) -s check-attribution
	@echo "  + CI checks passed"

.PHONY: ci-full
ci-full: ## quality: `ci` plus coverage gate, frontend tests, and integration tests
	@$(MAKE) -s ci
	@$(MAKE) -s coverage-gate
	@$(MAKE) -s test-frontend
	@$(MAKE) -s sdk-test-race
	@echo "  i the AUTHORITATIVE coverage number needs a database:"
	@echo "    DB_URL=postgres://… make coverage-gate-full   (also runs the integration suite)"

.PHONY: check
check: ci ## quality: Alias for `ci`

.PHONY: hooks-install
hooks-install: ## quality: Point git at .githooks/ (commit-msg + pre-commit + pre-push)
	@git config core.hooksPath .githooks
	@chmod +x .githooks/* 2>/dev/null || true
	@echo "  + git hooks enabled (core.hooksPath=.githooks)"
	@echo "    commit-msg: AI-attribution guard          (instant)"
	@echo "    pre-commit: gofmt · vet · .env guard      (~3s)"
	@echo "    pre-push:   make ci + make check-docs     (~60s)"
	@echo "    bypass with --no-verify; justify it in the PR"

.PHONY: hooks-uninstall
hooks-uninstall: ## quality: Stop using the repo's git hooks
	@git config --unset core.hooksPath || true
	@echo "  + git hooks disabled"

# =============================================================================
# Stack lifecycle
# =============================================================================

.PHONY: up
up: ## stack: Start postgres + api, against the Keycloak named in .env
	$(COMPOSE) up -d --build
	@echo "  + stack starting. Tail with 'make logs'"
	@echo "  i no Keycloak in this stack — it uses the one in KEYCLOAK_URL."
	@echo "    Want a throwaway one too? 'make up-eval'"

.PHONY: up-eval
up-eval: ## stack: `up`, plus the bundled throwaway Keycloak + mailpit (EVALUATION)
	$(COMPOSE) --profile dev-idp up -d --build
	@echo "  + evaluation stack starting. Console: http://localhost:8080/admin (adminuser / password)"

.PHONY: up-infra
up-infra: ## stack: Start only postgres + keycloak (skip the api container)
	$(COMPOSE) --profile dev-idp up -d postgres keycloak-postgres keycloak

.PHONY: stop
stop: ## stack: Stop containers without removing them (preserves containers + volumes + data)
	$(COMPOSE) stop
	@echo "  + containers stopped; containers, volumes, data, networks all preserved"
	@echo "  i resume with 'make up' or 'make start'"

.PHONY: start
start: ## stack: Start previously-stopped containers (no rebuild)
	$(COMPOSE) start
	@echo "  + containers resumed"

.PHONY: down
down: ## stack: Stop and remove containers (volumes + data preserved)
	$(COMPOSE) down
	@echo "  + containers + network removed; volumes + data preserved"
	@echo "  i recreate with 'make up' (data survives)"

.PHONY: purge
purge: ## stack: NUKE everything — containers, volumes, networks, bin/, api image (DATA LOSS)
	@printf "\033[1;31m\xe2\x9a\xa0\xef\xb8\x8f  This will DELETE all local data and docker volumes.\033[0m\n"
	@printf "    Includes: app postgres data, keycloak realm DB, bin/, the saas-api image.\n"
	@printf "Continue? [y/N] "; \
	read ans; \
	case "$$ans" in \
		y|Y|yes|YES) ;; \
		*) echo "  - aborted (nothing changed)"; exit 1 ;; \
	esac; \
	$(MAKE) -s _purge-run

.PHONY: _purge-run
# Internal: the actual purge actions. Split from `purge` so `reset-dev`
# can invoke the destruction without re-prompting (it has its own prompt).
_purge-run:
	@echo "  i tearing down stack + volumes + orphan containers..."
	-$(COMPOSE) down -v --remove-orphans
	@rm -rf $(BIN_DIR)
	@echo "  + removed $(BIN_DIR)/"
	@img=$$($(DOCKER) images -q lightweight-saas-backend-api 2>/dev/null | head -1); \
	if [ -n "$$img" ]; then \
		$(DOCKER) rmi -f $$img >/dev/null 2>&1 || true; \
		echo "  + removed local api image"; \
	else \
		echo "  i no local api image to remove"; \
	fi
	@echo "  + purge complete"

.PHONY: reset-dev
reset-dev: ## stack: One-command recovery — purge then rebuild then start (DATA LOSS)
	@printf "\033[1;31m\xe2\x9a\xa0\xef\xb8\x8f  reset-dev will DELETE all local data and recreate the stack from scratch.\033[0m\n"
	@printf "    Use this when Keycloak/JWKS is wedged, migrations are broken, or a volume is corrupted.\n"
	@printf "Continue? [y/N] "; \
	read ans; \
	case "$$ans" in \
		y|Y|yes|YES) ;; \
		*) echo "  - aborted (nothing changed)"; exit 1 ;; \
	esac; \
	$(MAKE) -s _purge-run; \
	echo "  i rebuilding + starting fresh stack..."; \
	$(COMPOSE) up -d --build; \
	echo "  + reset-dev complete. Follow boot with 'make logs', validate with 'make auth-test'."

.PHONY: logs
logs: ## stack: Tail logs from all services (Ctrl-C to exit)
	$(COMPOSE) logs -f --tail=100

# =============================================================================
# Keycloak
# =============================================================================

.PHONY: keycloak-export
keycloak-export: ## keycloak: Export the live 'saas' realm to deploy/keycloak/
	$(COMPOSE) exec keycloak /opt/keycloak/bin/kc.sh export \
		--dir /opt/keycloak/data/import --realm saas --users realm_file

.PHONY: keycloak-import
keycloak-import: ## keycloak: Restart keycloak so it re-imports realm-export.json
	$(COMPOSE) restart keycloak

.PHONY: realm-reset
realm-reset: ## keycloak: Wipe keycloak DB and re-import realm (DATA LOSS for KC)
	@printf "About to delete the Keycloak database. Continue? [y/N] "; \
	read ans; [ "$$ans" = "y" ] || { echo "aborted"; exit 1; }
	$(COMPOSE) stop keycloak keycloak-postgres
	$(DOCKER) volume rm $$(basename $$(pwd))_keycloak_postgres_data 2>/dev/null || true
	$(COMPOSE) up -d keycloak-postgres keycloak

# =============================================================================
# Database
# =============================================================================

.PHONY: migrate
migrate: ## db: Apply all pending migrations
	$(GO) run ./cmd/migrate up

.PHONY: migrate-version
migrate-version: ## db: Print the applied schema version (exits non-zero if dirty)
	@$(GO) run ./cmd/migrate version

.PHONY: migrate-down
migrate-down: ## db: Revert ALL migrations — drops every table (DATA LOSS)
	@printf "About to revert every migration, dropping all tables. Continue? [y/N] "; \
	read ans; [ "$$ans" = "y" ] || { echo "aborted"; exit 1; }
	$(GO) run ./cmd/migrate down

.PHONY: migrate-steps
migrate-steps: ## db: Apply N migrations (N=-1 reverts one). Usage: make migrate-steps N=-1
	@[ -n "$(N)" ] || { echo "  ✗ usage: make migrate-steps N=-1"; exit 1; }
	$(GO) run ./cmd/migrate steps $(N)

.PHONY: migrate-force
migrate-force: ## db: RECOVERY ONLY — record VERSION and clear the dirty flag without running SQL
	@[ -n "$(VERSION)" ] || { echo "  ✗ usage: make migrate-force VERSION=1"; exit 1; }
	@echo "  ! this records a version WITHOUT running SQL — see docs/MIGRATIONS.md"
	$(GO) run ./cmd/migrate force $(VERSION)

.PHONY: migrate-new
migrate-new: ## db: Scaffold an up/down migration pair. Usage: make migrate-new NAME=add_workspaces
	@[ -n "$(NAME)" ] || { echo "  ✗ usage: make migrate-new NAME=add_workspaces"; exit 1; }
	@./scripts/new_migration.sh "$(NAME)"

# =============================================================================
# Secret keyring
# =============================================================================

# Built rather than `go run`, and that is not a preference.
#
# `go run` reports a non-zero program exit as `exit status N` on stderr and then
# exits 1 itself, collapsing this command's three-way contract — 0 success, 1 a
# row failed, 2 bad invocation — into two values. A deploy script branching on
# the difference would be branching on nothing.
SECRETS_BINARY := $(BIN_DIR)/secrets

.PHONY: secrets-build
secrets-build: ## secrets: Compile the keyring CLI to bin/secrets
	@mkdir -p $(BIN_DIR)
	@$(GO) build -trimpath -o $(SECRETS_BINARY) ./cmd/secrets

.PHONY: secrets-status
secrets-status: secrets-build ## secrets: Which key versions the stored credentials need, and which keys are safe to remove
	@$(SECRETS_BINARY) status

.PHONY: secrets-rotate
secrets-rotate: secrets-build ## secrets: Re-seal every stored provider credential under the current key (idempotent)
	@$(SECRETS_BINARY) rotate

.PHONY: secrets-rotate-dry-run
secrets-rotate-dry-run: secrets-build ## secrets: Report what a rotation would do, without decrypting or writing
	@$(SECRETS_BINARY) rotate --dry-run

.PHONY: secrets-genkey
secrets-genkey: ## secrets: Print a fresh base64 32-byte key for SECRETS_KEYRING
	@openssl rand -base64 32

.PHONY: secrets-check
secrets-check: ## e2e: Key-lifecycle boundary — real CLI, real keys, artifact scan. Requires DB_URL (throwaway).
	@[ -n "$${DB_URL:-}" ] || { \
		echo "  - DB_URL is required, and must point at a THROWAWAY database."; \
		echo "    Bring the stack up with 'make up-infra' first, then:"; \
		echo "    DB_URL=postgres://saas:saas@localhost:5432/saas_secrets?sslmode=disable make secrets-check"; \
		exit 1; }
	@./scripts/secrets-rotation-check.sh

.PHONY: seed
seed: ## db: Seed initial data (Keycloak realm export only — the app DB has no seeds)
	@echo "  i the application database has NO seed data (single 'users' table, populated"
	@echo "    just-in-time by user.Service.EnsureUser on first authenticated request)"
	@echo "  i seed identities come from Keycloak via deploy/keycloak/realm-export.json"

# =============================================================================
# Bootstrap / Project config
# =============================================================================

.PHONY: init-env
init-env: ## install: Create .env and generate the secrets keyring (no Go, no prompts)
	@./scripts/init.sh

.PHONY: init
init: ## bootstrap: FORK TOOL — re-derive project.json/.env.example/realm export (prompts, needs Go)
	$(GO) run ./cmd/bootstrap

.PHONY: regen
regen: ## bootstrap: FORK TOOL — same as `init` without prompts, from $(CONFIG_JSON)
	$(GO) run ./cmd/bootstrap -non-interactive

# =============================================================================
# Auth / E2E
# =============================================================================

.PHONY: auth-test
auth-test: ## e2e: Acquire a Keycloak token and call /me (requires curl + jq)
	@command -v jq >/dev/null   || { echo "  - jq required";   exit 1; }
	@command -v curl >/dev/null || { echo "  - curl required"; exit 1; }
	@./scripts/auth-test.sh

.PHONY: e2e
e2e: ## e2e: Start the stack and run end-to-end smoke test
	@$(MAKE) -s up
	@./scripts/e2e.sh

.PHONY: e2e-m2m
e2e-m2m: ## e2e: Machine boundary — external backend against the real API. Requires DB_URL (throwaway).
	@[ -n "$${DB_URL:-}" ] || { \
		echo "  - DB_URL is required, and must point at a THROWAWAY database."; \
		echo "    Bring the stack up with 'make up-infra' first, then:"; \
		echo "    DB_URL=postgres://saas:saas@localhost:5432/saas_e2e?sslmode=disable make e2e-m2m"; \
		exit 1; }
	@./scripts/m2m-harness.sh

.PHONY: e2e-negative-authz
e2e-negative-authz: ## e2e: Negative authorization matrix (KI-018) — real Postgres + Keycloak. Requires DB_URL (throwaway).
	@[ -n "$${DB_URL:-}" ] || { \
		echo "  - DB_URL is required, and must point at a THROWAWAY database."; \
		echo "    Bring the stack up with 'make up-infra' first, then:"; \
		echo "    DB_URL=postgres://saas:saas@localhost:5432/saas_negauthz?sslmode=disable make e2e-negative-authz"; \
		exit 1; }
	@./scripts/negative-authz-e2e.sh

.PHONY: e2e-browser
e2e-browser: ## e2e: Operator boundary — real Chromium through the console. Requires DB_URL (throwaway).
	@command -v npm >/dev/null || { echo "  - npm required (Playwright toolchain)"; exit 1; }
	@[ -n "$${DB_URL:-}" ] || { \
		echo "  - DB_URL is required, and must point at a THROWAWAY database."; \
		echo "    Bring the stack up with 'make up-infra' first, then:"; \
		echo "    DB_URL=postgres://saas:saas@localhost:5432/saas_browser?sslmode=disable make e2e-browser"; \
		echo; \
		echo "    Re-running against the same database? Add --reset-db:"; \
		echo "    DB_URL=... ./scripts/browser-e2e.sh --reset-db"; \
		exit 1; }
	@./scripts/browser-e2e.sh

.PHONY: e2e-browser-headed
e2e-browser-headed: ## e2e: `e2e-browser` with a visible browser, for debugging a failure locally
	@[ -n "$${DB_URL:-}" ] || { echo "  - DB_URL is required (throwaway database)"; exit 1; }
	@./scripts/browser-e2e.sh --headed --reset-db

# =============================================================================
# Swagger
# =============================================================================

.PHONY: swagger
swagger: ## docs: Regenerate Swagger docs from annotations (writes docs/{docs.go,swagger.json,swagger.yaml})
	@command -v swag >/dev/null || $(GO) install github.com/swaggo/swag/cmd/swag@latest
	@swag init -g cmd/api/main.go --output docs --quiet
	@echo "  + regenerated docs/{docs.go,swagger.json,swagger.yaml}"

.PHONY: docs
docs: swagger ## docs: Alias for `swagger`

.PHONY: docs-clean
docs-clean: ## docs: Remove generated Swagger artifacts (next `make docs` recreates them)
	@rm -f docs/docs.go docs/swagger.json docs/swagger.yaml
	@echo "  + removed docs/docs.go docs/swagger.json docs/swagger.yaml (run 'make docs' to regenerate)"

.PHONY: swagger-check
swagger-check: ## docs: CI gate — fail if committed Swagger docs are out of sync with annotations
	@command -v swag >/dev/null || $(GO) install github.com/swaggo/swag/cmd/swag@latest
	@swag init -g cmd/api/main.go --output docs --quiet
	@if ! git diff --quiet -- docs/swagger.json docs/swagger.yaml docs/docs.go 2>/dev/null; then \
		echo "  - committed Swagger docs are stale. Run 'make docs' and commit the result."; \
		echo "  i drift detected in:"; \
		git diff --name-only -- docs/swagger.json docs/swagger.yaml docs/docs.go | sed 's/^/    /'; \
		exit 1; \
	fi
	@echo "  + swagger.{json,yaml,docs.go} match annotations"
