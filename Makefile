.PHONY: help install install-py install-js sync lock \
        test test-py test-js test-e2e test-docker test-desktop-e2e \
        lint lint-py lint-js fix \
        build build-web build-tui build-website build-desktop \
        run run-tui run-gateway run-dashboard run-serve \
        run-desktop-dev run-tui-dev run-website-dev \
        pack-docker release release-publish \
        dist-desktop dist-desktop-mac dist-desktop-win dist-desktop-linux pack-desktop \
        clean

# ── Config ──────────────────────────────────────────────────────────
PYTHON ?= 3.11
UV     ?= uv
NPM    ?= npm

# ── Help ────────────────────────────────────────────────────────────
help: ## Show available targets
	@grep -E '^[a-zA-Z0-9_.-]+:.*##' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'

# ── Install ─────────────────────────────────────────────────────────
install: install-py install-js ## Install Python + Node deps (dev)

install-py: ## Python editable install (CI-parity: locked sync)
	$(UV) sync --locked --python $(PYTHON) --extra all --extra dev

install-js: ## npm workspaces (CI uses npm ci)
	$(NPM) ci

sync: install-py ## Alias for install-py

lock: ## Regenerate uv.lock after pyproject.toml changes
	$(UV) lock

# ── Test ────────────────────────────────────────────────────────────
test: test-py test-js ## Run all tests (Python + JS)

test-py: ## Python suite (CI-parity; prefer over bare pytest)
	scripts/run_tests.sh

test-py-file: ## Run one test file: make test-py-file FILE=tests/foo.py
	@test -n "$(FILE)" || (echo "Usage: make test-py-file FILE=tests/foo.py" && exit 1)
	scripts/run_tests.sh $(FILE)

test-js: ## All npm workspace check scripts
	$(NPM) run check

test-e2e: install-py ## Python E2E tests
	$(UV) run python -m pytest tests/e2e/ -v --tb=short

test-docker: install-py ## Docker integration tests (requires local image)
	scripts/run_tests.sh tests/docker/ --file-timeout 600

test-desktop-e2e: build-desktop ## Desktop Playwright E2E
	cd apps/desktop && $(NPM) run test:e2e

# ── Lint ────────────────────────────────────────────────────────────
lint: lint-py lint-js ## Lint Python + JS

lint-py: ## ruff (blocking) + ty (advisory)
	ruff check .
	ty check

lint-js: ## ESLint/typecheck via workspace checks
	$(NPM) run check

fix: ## Auto-fix JS formatting/lint
	$(NPM) run fix

# ── Build ─────────────────────────────────────────────────────────
build: build-web build-tui ## Build dashboard + TUI frontend artifacts

build-web: install-js ## Dashboard SPA -> web/dist/
	cd web && $(NPM) run build

build-tui: install-js ## Ink TUI bundle
	cd ui-tui && $(NPM) run build

build-website: install-js ## Docusaurus docs site
	cd website && $(NPM) run build

build-desktop: install-js ## Electron renderer + main
	cd apps/desktop && $(NPM) run build

# ── Run ─────────────────────────────────────────────────────────────
run: ## Interactive CLI
	$(UV) run hermes

run-tui: build-tui ## TUI mode
	$(UV) run hermes --tui

run-gateway: ## Messaging gateway (foreground)
	$(UV) run hermes gateway

run-dashboard: build-web ## Web dashboard
	$(UV) run hermes dashboard

run-serve: build-web ## Headless backend (desktop/dashboard API)
	$(UV) run hermes serve

run-desktop-dev: ## Desktop dev (Vite + Electron)
	cd apps/desktop && $(NPM) run dev

run-tui-dev: ## TUI watch mode
	cd ui-tui && $(NPM) run dev

run-website-dev: ## Docs site dev server
	cd website && $(NPM) start

# ── Package / Release ───────────────────────────────────────────────
pack-docker: ## Build Docker image locally
	docker build -t hermes-agent:local .

release: ## Preview release changelog (dry run)
	$(UV) run python scripts/release.py

release-publish: ## Publish release: make release-publish BUMP=minor
	@test -n "$(BUMP)" || (echo "Usage: make release-publish BUMP=minor|patch|major" && exit 1)
	$(UV) run python scripts/release.py --bump $(BUMP) --publish

dist-desktop: build-desktop ## Desktop installers (platform-specific)
	cd apps/desktop && $(NPM) run dist

dist-desktop-mac: build-desktop ## macOS desktop installers
	cd apps/desktop && $(NPM) run dist:mac

dist-desktop-win: build-desktop ## Windows desktop installers
	cd apps/desktop && $(NPM) run dist:win

dist-desktop-linux: build-desktop ## Linux desktop installers
	cd apps/desktop && $(NPM) run dist:linux

pack-desktop: build-desktop ## Unpacked desktop app (--dir)
	cd apps/desktop && $(NPM) run pack

# ── Clean ───────────────────────────────────────────────────────────
clean: ## Remove common build artifacts
	rm -rf web/dist ui-tui/dist apps/desktop/dist website/build \
	       apps/desktop/out .pytest_cache .ruff_cache
