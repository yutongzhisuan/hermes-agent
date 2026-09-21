# Headless Wheel — Offline Bundles (Frozen / Vendored)

Two offline distribution formats complement the online pip wheel (`dist/xhermes_agent-*.whl`).

## Comparison

| Format | Output | Size (typ.) | Target requirements | Offline install |
|--------|--------|-------------|---------------------|-----------------|
| **Online wheel** | `xhermes_agent-*.whl` | ~15 MB | Python 3.11+, network pip | `pip install *.whl` |
| **Frozen venv** | `xhermes-agent-frozen-py3.11-<platform>.tar.gz` | ~700 MB | Same OS/arch, Python 3.11 venv-compatible | Extract → `./bin/serve` |
| **Vendored wheels** | `xhermes-agent-vendored-py3.11-<platform>.tar.gz` | ~180 MB | Python 3.11+ with `venv` + `pip` | Extract → `./install.sh` |

Neither offline bundle embeds a Python interpreter. For a fully self-contained runtime (interpreter + libs), use Nix/Docker instead.

## Build

```bash
make help-offline-platforms   # per-OS/arch commands
make dist-offline             # both frozen + vendored (current host)
make dist-frozen              # frozen venv tarball only
make dist-vendored            # vendored wheels tarball only
```

### Per-platform targets

| Platform | Command | Where to run |
|----------|---------|--------------|
| macOS Apple Silicon | `make dist-offline-macos-arm64` | Native |
| macOS Intel | `make dist-offline-macos-x86_64` | Native; Rosetta on arm64 Mac |
| Linux x86_64 | `make dist-offline-linux-x86_64` | Native on Linux amd64; Docker elsewhere |
| Linux arm64 | `make dist-offline-linux-aarch64` | Native on Linux arm64; Docker elsewhere |
| Windows x86_64 | `make dist-offline-windows-x86_64` | Native (Git Bash / WSL) |

Linux Docker image override: `OFFLINE_DOCKER_IMAGE=<uv-image>`.

### CI (GitHub Actions)

Workflow: [`.github/workflows/headless-wheel-offline.yml`](../../.github/workflows/headless-wheel-offline.yml)

```bash
# Manual run (all platforms) from GitHub → Actions → "Headless wheel offline bundles"
# Or via gh CLI:
gh workflow run headless-wheel-offline.yml

# Single platform:
gh workflow run headless-wheel-offline.yml -f platform=linux-x86_64
```

The workflow builds a 5-platform matrix (macOS arm64/x86_64, Linux x86_64/arm64, Windows x86_64), smoke-tests each frozen bundle, uploads per-platform artifacts plus a merged tarball. On **Release published**, bundles are attached as release assets automatically.

Not part of the PR CI gate (too large/slow). Reusable via `workflow_call` from release automation.

Artifacts land in `dist/`:

```
dist/xhermes-agent-frozen-py3.11-macos-arm64.tar.gz
dist/xhermes-agent-vendored-py3.11-macos-arm64.tar.gz
```

Platform tag comes from `uname` (`macos-arm64`, `linux-x86_64`, …). Python version defaults to `PYTHON=3.11` in the Makefile.

## Frozen venv bundle

Pre-built **relocatable** virtualenv (`uv venv --relocatable`) with `xhermes-agent` and **all core dependencies** already installed.

```bash
tar xzf dist/xhermes-agent-frozen-py3.11-macos-arm64.tar.gz
cd xhermes-agent-frozen-py3.11-macos-arm64
./bin/serve --host 127.0.0.1 --port 0
# or
./bin/xhermes serve
```

Best when the target machine matches the build platform and you want zero install steps.

## Vendored wheels bundle

Directory of pre-downloaded `.whl` files plus an offline installer.

```bash
tar xzf dist/xhermes-agent-vendored-py3.11-macos-arm64.tar.gz
cd xhermes-agent-vendored-py3.11-macos-arm64
./install.sh          # creates ./venv, pip install --no-index
./venv/bin/xhermes serve
```

Best for air-gapped sites that already manage Python/venv but block PyPI.

## What's included

Same **core dependency set** as the headless online wheel (serve, gateway, MCP, messaging, voice/wake, etc.). Optional extras (`matrix`, `honcho`, `google`, …) are **not** bundled — install separately when online.

## Limitations

- Bundles are **platform-specific** (wheels match build OS/CPU).
- **Not** a single PyInstaller binary; Python 3.11+ must exist on the target host.
- API keys / `~/.xhermes/config.yaml` are still required at runtime.
