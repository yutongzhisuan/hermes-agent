.PHONY: help install sync lock \
        test test-unit test-integration test-file \
        lint build dist-wheel clean run-serve

# Headless wheel workflow only (no desktop / web / TUI / npm targets).

PYTHON ?= 3.11
UV     ?= uv

# ── Help ────────────────────────────────────────────────────────────
help: ## Show available targets
	@grep -E '^[a-zA-Z0-9_.-]+:.*##' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'

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
	HERMES_HEADLESS_WHEEL_BUILD=1 scripts/build_headless_wheel.sh

# ── Run (dev from source tree) ──────────────────────────────────────
run-serve: ## Headless JSON-RPC backend (dev; same protocol as wheel)
	$(UV) run xhermes serve

# ── Clean ───────────────────────────────────────────────────────────
clean: ## Remove wheel output and local build staging
	rm -rf dist .pytest_cache .ruff_cache \
	       xhermes_agent_data/skills xhermes_agent_data/optional-skills \
	       xhermes_agent_data/locales xhermes_agent_data/optional-mcps \
	       xhermes_agent_data/.headless_wheel_dist
