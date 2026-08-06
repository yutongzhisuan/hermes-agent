.PHONY: help help-offline-platforms install sync lock \
        test test-unit test-integration test-file \
        lint build dist-wheel dist-frozen dist-vendored dist-offline clean run-serve \
        dist-offline-macos-arm64 dist-offline-macos-x86_64 \
        dist-offline-linux-x86_64 dist-offline-linux-aarch64 \
        dist-offline-windows-x86_64

# Headless wheel workflow only (no desktop / web / TUI / npm targets).

PYTHON ?= 3.11
UV     ?= uv

# ── Help ────────────────────────────────────────────────────────────
help: ## Show available targets
	@grep -E '^[a-zA-Z0-9_.-]+:.*##' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-28s\033[0m %s\n", $$1, $$2}'

help-offline-platforms: ## Per-OS/arch offline bundle build commands
	@echo "Offline bundles are platform-specific (wheels match build OS/CPU)."
	@echo "Artifacts: dist/xhermes-agent-{frozen,vendored}-py$(PYTHON)-<platform>.tar.gz"
	@echo ""
	@echo "Platform          Command                              Notes"
	@echo "----------------  -----------------------------------  ------------------------------"
	@echo "macos-arm64       make dist-offline-macos-arm64        Native on Apple Silicon Mac"
	@echo "macos-x86_64      make dist-offline-macos-x86_64       Intel Mac; Rosetta on arm64 Mac"
	@echo "linux-x86_64      make dist-offline-linux-x86_64       Native on Linux amd64; else Docker"
	@echo "linux-aarch64     make dist-offline-linux-aarch64      Native on Linux arm64; else Docker"
	@echo "windows-x86_64    make dist-offline-windows-x86_64     Native on Windows (Git Bash / WSL)"
	@echo ""
	@echo "Generic (current host platform tag from uname):"
	@echo "  make dist-offline          # frozen + vendored"
	@echo "  make dist-frozen           # frozen venv only"
	@echo "  make dist-vendored         # vendored wheels only"
	@echo ""
	@echo "Linux Docker env override: OFFLINE_DOCKER_IMAGE=<uv-image>"

# ── Install ─────────────────────────────────────────────────────────
install: ## Python dev deps for wheel build/test
	$(UV) sync --locked --python $(PYTHON) --extra dev

sync: install ## Alias for install

lock: ## Regenerate uv.lock after pyproject.toml changes
	$(UV) lock

# ── Test ────────────────────────────────────────────────────────────
test: test-unit test-integration ## All headless wheel tests

test-unit: install ## Wheel unit tests (runtime, packaging, guards)
	scripts/run_tests.sh \
		tests/hermes_runtime/test_runtime.py \
		tests/hermes_runtime/test_rpc.py \
		tests/hermes_cli/test_headless_ui_guards.py \
		tests/test_packaging_build_guard.py \
		tests/test_project_metadata.py -q

test-integration: install ## Wheel integration tests (build + install + smoke)
	scripts/run_tests.sh \
		tests/test_headless_wheel_assets.py \
		tests/hermes_runtime/test_wheel_smoke.py \
		-m integration -q

test-file: install ## Run one test file: make test-file FILE=tests/foo.py
	@test -n "$(FILE)" || (echo "Usage: make test-file FILE=tests/foo.py" && exit 1)
	scripts/run_tests.sh $(FILE)

# ── Lint ────────────────────────────────────────────────────────────
lint: ## ruff (blocking) + ty (advisory) on Python sources
	ruff check .
	ty check

# ── Build / Package ─────────────────────────────────────────────────
build: dist-wheel ## Alias: build headless pip wheel

dist-wheel: ## Headless pip wheel -> dist/xhermes_agent-*.whl
	XHERMES_HEADLESS_WHEEL_BUILD=1 scripts/build_headless_wheel.sh

dist-frozen: ## Offline frozen venv tarball (all deps pre-installed)
	PYTHON=$(PYTHON) scripts/build_offline_bundle.sh frozen

dist-vendored: ## Offline vendored wheels tarball (air-gapped pip install)
	PYTHON=$(PYTHON) scripts/build_offline_bundle.sh vendored

dist-offline: ## Both offline bundles (frozen + vendored) for current host
	PYTHON=$(PYTHON) scripts/build_offline_bundle.sh all

# ── Offline bundles by target OS/arch ───────────────────────────────
# Online wheel (py3-none-any) is platform-independent; targets below are for
# frozen/vendored offline bundles only.

dist-offline-macos-arm64: install ## Offline bundles for macOS Apple Silicon (native)
	scripts/check_offline_platform.sh macos-arm64
	$(MAKE) dist-offline

dist-offline-macos-x86_64: install ## Offline bundles for macOS Intel (Rosetta on arm64)
	@if [ "$$(uname -s | tr '[:upper:]' '[:lower:]')" != "darwin" ]; then \
		echo "ERROR: macos-x86_64 must be built on macOS" >&2; exit 1; \
	fi
	@if [ "$$(uname -m)" = "arm64" ]; then \
		echo "→ Building macos-x86_64 under Rosetta (arch -x86_64)..."; \
		arch -x86_64 $(MAKE) PYTHON=$(PYTHON) dist-offline; \
	else \
		scripts/check_offline_platform.sh macos-x86_64; \
		$(MAKE) dist-offline; \
	fi

dist-offline-linux-x86_64: install ## Offline bundles for Linux x86_64 (Docker if not native)
	@if [ "$$(uname -s)" = "Linux" ] && [ "$$(uname -m)" = "x86_64" ]; then \
		scripts/check_offline_platform.sh linux-x86_64; \
		$(MAKE) dist-offline; \
	else \
		scripts/build_offline_docker.sh x86_64 all; \
	fi

dist-offline-linux-aarch64: install ## Offline bundles for Linux arm64 (Docker if not native)
	@if [ "$$(uname -s)" = "Linux" ] && { [ "$$(uname -m)" = "aarch64" ] || [ "$$(uname -m)" = "arm64" ]; }; then \
		scripts/check_offline_platform.sh linux-aarch64; \
		$(MAKE) dist-offline; \
	else \
		scripts/build_offline_docker.sh aarch64 all; \
	fi

dist-offline-windows-x86_64: install ## Offline bundles for Windows x86_64 (native only)
	@scripts/check_offline_platform.sh windows-x86_64
	$(MAKE) dist-offline

# ── Run (dev from source tree) ──────────────────────────────────────
run-serve: ## Headless JSON-RPC backend (dev; same protocol as wheel)
	$(UV) run xhermes serve

# ── Clean ───────────────────────────────────────────────────────────
clean: ## Remove wheel output, offline bundles, and local build staging
	rm -rf dist .pytest_cache .ruff_cache __pycache__ dist/.offline-staging-* \
	       xhermes_agent_data/skills xhermes_agent_data/optional-skills \
	       xhermes_agent_data/locales xhermes_agent_data/optional-mcps \
	       xhermes_agent_data/.headless_wheel_dist
