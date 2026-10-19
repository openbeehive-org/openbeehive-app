# Task-runner contract (https://fleetlint.org/baseline/#task-runner-contract). `make check` is the definition of green.
#
# Fresh checkout:
#   make tools      # pinned Go gate tools into $(go env GOPATH)/bin; gitleaks comes from its own release
#   make proto      # Go + TS stubs from proto/ (neither is committed; CI and the Dockerfile regenerate them)
#   make check      # lint, tests, coverage, audit, build, fleetlint
#
# Runtime prerequisites: Go (server/go.mod), Node (app/.nvmrc), buf (BUF_VERSION, installed by make tools).
SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.ONESHELL:
.DEFAULT_GOAL := check-fast
MAIN_BRANCH ?= main

GO      ?= go
BUF     ?= buf
SERVER  := server
APP     := app
# Go statement coverage across all packages (-coverpkg), internal/gen excluded. Honest floor just below
# today's number (54%): the HTTP auth handlers and the blob/web adapters have few tests yet. Raise it as tests land.
COVER_MIN ?= 53
LICENSES  ?= MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0
# Each pin carries a `# renovate:` comment so Renovate bumps it (customManagers:makefileVersions in renovate.json).
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_VERSION    ?= v2.15.0
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION ?= v1.9.0
# renovate: datasource=go depName=github.com/google/go-licenses/v2
GO_LICENSES_VERSION ?= v2.0.1
# renovate: datasource=go depName=github.com/fleetlint/fleetlint
FLEETLINT_VERSION   ?= v0.5.1
# renovate: datasource=go depName=github.com/bufbuild/buf
BUF_VERSION         ?= v1.74.0
# Tools are built with this module's toolchain: golangci-lint refuses code that targets a newer Go than it was built with.
TOOLCHAIN := $(shell cd $(SERVER) && $(GO) env GOVERSION)

.PHONY: help tools proto proto-lint proto-breaking gen fmt lint test cover audit build dist check-fast check release \
	tidy clean run-server dev-app app-deps app-lint app-test app-cover app-audit app-build

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*##' '{printf "  %-16s %s\n", $$1, $$2}'

# ----------------------------------------------------------------------------
# Contract targets
# ----------------------------------------------------------------------------

tools: ## install the pinned gate tools (Go tools + buf; Node tools come from app/package-lock.json)
	cd $(SERVER)
	GOTOOLCHAIN=$(TOOLCHAIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) $(GO) install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)
	$(GO) install github.com/fleetlint/fleetlint/cmd/fleetlint@$(FLEETLINT_VERSION)
	$(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

fmt: gen app-deps ## format in place (Go + app)
	(cd $(SERVER) && golangci-lint fmt)
	(cd $(APP) && npm run format)

lint: gen proto-lint app-lint ## format check + linters + vet (Go), prettier + eslint + svelte-check (app)
	cd $(SERVER)
	golangci-lint fmt --diff
	golangci-lint run
	$(GO) vet ./...

test: gen app-test ## unit tests: Go with the race detector (shuffled) + app vitest
	cd $(SERVER) && $(GO) test -race -shuffle=on -count=1 ./...

cover: gen app-cover ## coverage gates: Go total (internal/gen excluded) >= COVER_MIN, app thresholds in vitest.config.ts
	cd $(SERVER)
	coverpkg=$$($(GO) list ./... | grep -v /internal/gen/ | paste -sd, -)
	# -coverpkg makes each line list every package; keep the summary short.
	$(GO) test -count=1 -coverpkg="$$coverpkg" -coverprofile=cover.raw.out ./... | sed 's/ of statements in .*//'
	grep -v '/internal/gen/' cover.raw.out > cover.out
	total=$$($(GO) tool cover -func=cover.out | awk '/^total:/ {sub("%", "", $$3); print $$3}')
	echo "coverage $$total% (minimum $(COVER_MIN)%)"
	if [ "$${total%.*}" -lt "$(COVER_MIN)" ]; then echo "coverage below $(COVER_MIN)%"; exit 1; fi

# go-licenses spots stdlib packages by GOROOT; pass the active toolchain's, not the one it was built with
audit: gen app-audit ## vulnerabilities, licenses, tidy module graph, secrets
	cd $(SERVER)
	govulncheck ./...
	GOROOT=$$($(GO) env GOROOT) go-licenses check ./... --ignore github.com/openbeehive-org/openbeehive-app --allowed_licenses=$(LICENSES)
	go mod tidy -diff
	cd .. && gitleaks git --no-banner --redact .

build: gen app-build ## build the app, embed it, compile the single binary at server/bin/openbeehive
	# keep the tracked .gitkeep: the embed must compile before the app is built
	find $(SERVER)/internal/web/dist -mindepth 1 -not -name .gitkeep -delete
	cp -r $(APP)/build/. $(SERVER)/internal/web/dist/
	cd $(SERVER) && CGO_ENABLED=0 $(GO) build -trimpath -o bin/openbeehive ./cmd/server
	@echo "built $(SERVER)/bin/openbeehive (SPA embedded)"

dist: ## the release image, locally (CI builds and pushes it in release.yml)
	$(or $(shell command -v podman 2>/dev/null),docker) build -t ghcr.io/openbeehive-org/openbeehive-app:dev .

check-fast: gen ## before every commit (< 30 s)
	cd $(SERVER)
	golangci-lint fmt --diff
	golangci-lint run --new-from-rev=origin/$(MAIN_BRANCH) || golangci-lint run
	$(GO) test -short ./...

check: proto-lint proto-breaking lint test cover audit build ## definition of green (< 10 min)
	fleetlint check --fail-on error

release: ## refuse dirty/non-main -> check -> signed tag V (its CHANGELOG.md section must exist); pushing the tag publishes the image
	@test -n "$(V)" || { echo "usage: make release V=vX.Y.Z"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "dirty tree"; exit 1; }
	@test "$$(git branch --show-current)" = "$(MAIN_BRANCH)" || { echo "not on $(MAIN_BRANCH)"; exit 1; }
	@grep -q "^## \[$(V:v%=%)\]" CHANGELOG.md || { echo "CHANGELOG.md has no '## [$(V:v%=%)]' section"; exit 1; }
	$(MAKE) check
	# tag body = the CHANGELOG section; release.yml puts it at the top of the release notes
	notes=$$(mktemp); { echo "$(V)"; echo; awk '/^## \[$(V:v%=%)\]/{f=1;next} /^## \[/{f=0} f' CHANGELOG.md; } > "$$notes"; git tag -s "$(V)" -F "$$notes"; rm -f "$$notes"
	@echo "tagged $(V), push with: git push origin $(V)"

tidy: ## go mod tidy
	cd $(SERVER) && $(GO) mod tidy

clean: ## remove build, coverage and generated output
	rm -rf $(APP)/build $(APP)/coverage $(SERVER)/bin $(SERVER)/cover.out $(SERVER)/cover.raw.out
	# keep the tracked .gitkeep: the embed must compile before the app is built
	find $(SERVER)/internal/web/dist -mindepth 1 -not -name .gitkeep -delete
	touch $(SERVER)/internal/web/dist/.gitkeep

# ----------------------------------------------------------------------------
# Proto: the .proto contract is the API's source of truth. Stubs are generated
# into server/internal/gen (Go) and app/src/lib/proto (TS); neither is committed.
# ----------------------------------------------------------------------------

proto: ## generate Go + TypeScript stubs (remote plugins pinned in buf.gen.yaml)
	$(BUF) generate

# Generate once on a fresh checkout; `make proto` regenerates after contract changes.
gen:
	test -d $(SERVER)/internal/gen/openbeehive && test -d $(APP)/src/lib/proto || $(MAKE) proto

proto-lint: ## buf lint
	$(BUF) lint

# The proto contract is the public API (scripts and devices build against it).
proto-breaking: ## buf breaking against the previous release tag (needs tags: fetch-depth 0 in CI)
	base=$$(git describe --tags --abbrev=0 --match 'v*')
	$(BUF) breaking --against ".git#tag=$$base"

# ----------------------------------------------------------------------------
# App (SvelteKit SPA in app/; tool versions pinned by app/package-lock.json)
# ----------------------------------------------------------------------------

# npm ci reruns when the lockfile changes.
app-deps: $(APP)/node_modules/.package-lock.json

$(APP)/node_modules/.package-lock.json: $(APP)/package-lock.json
	cd $(APP) && npm ci

app-lint: app-deps ## prettier + eslint + svelte-check
	cd $(APP)
	npm run lint
	npm run check

app-test: app-deps ## vitest
	cd $(APP) && npm test

app-cover: app-deps ## vitest with coverage thresholds
	cd $(APP) && npm run cover

app-audit: app-deps ## npm audit (high+), licenses, unused dependencies
	cd $(APP)
	npm audit --audit-level=high
	npm run licenses
	npm run unused

app-build: app-deps ## static SPA build into app/build
	cd $(APP) && npm run build

# ----------------------------------------------------------------------------
# Local dev: run the backend and the frontend in separate shells
# ----------------------------------------------------------------------------

run-server: gen ## backend on :8080 (loads .env if present)
	cd $(SERVER) && set -a && [ -f ../.env ] && . ../.env; set +a; $(GO) run ./cmd/server

dev-app: app-deps ## Vite dev server on :5173
	cd $(APP) && npm run dev
