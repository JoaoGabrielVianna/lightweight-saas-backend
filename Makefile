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

.PHONY: lint
lint: ## quality: Run golangci-lint if available, otherwise fmt-check
	@if command -v golangci-lint >/dev/null; then \
		golangci-lint run; \
	else \
		echo "  ! golangci-lint not installed; running fmt-check as fallback"; \
		$(MAKE) -s fmt-check; \
	fi

.PHONY: ci
ci: ## quality: All checks CI must pass — fmt-check + vet + build + test + swagger-check
	@$(MAKE) -s fmt-check
	@$(MAKE) -s vet
	@$(MAKE) -s build
	@$(MAKE) -s test
	@$(MAKE) -s swagger-check
	@echo "  + CI checks passed"

.PHONY: check
check: ci ## quality: Alias for `ci`

# =============================================================================
# Stack lifecycle
# =============================================================================

.PHONY: up
up: ## stack: Start the full docker-compose stack (postgres + keycloak + api)
	$(COMPOSE) up -d --build
	@echo "  + stack starting. Tail with 'make logs'"

.PHONY: up-infra
up-infra: ## stack: Start only postgres + keycloak (skip the api container)
	$(COMPOSE) up -d postgres keycloak-postgres keycloak

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
migrate: ## db: Run migrations (currently handled by gorm AutoMigrate on app start)
	@echo "  i migrations run automatically via database.Connect() on app startup"

.PHONY: seed
seed: ## db: Seed initial data (currently handled by database.seedDefaultUser on app start)
	@echo "  i app seeds a default user on startup; Keycloak seeds via deploy/keycloak/realm-export.json"

# =============================================================================
# Bootstrap / Project config
# =============================================================================

.PHONY: init
init: ## bootstrap: Run the interactive project bootstrap CLI
	$(GO) run ./cmd/bootstrap

.PHONY: regen
regen: ## bootstrap: Regenerate .env and realm-export.json from $(CONFIG_JSON) (no prompts)
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
