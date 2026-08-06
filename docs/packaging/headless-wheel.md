# Headless Agent Wheel

Third-party desktop apps integrate with XHermes via a **headless Python wheel**
that ships the full agent backend without TUI, Electron, or dashboard SPA assets.

## Install

```bash
pip install xhermes-agent
```

Requires Python 3.11–3.13.

## Quick start (Python SDK)

```python
from hermes_runtime import HermesRuntime

with HermesRuntime() as rt:
    info = rt.start()
    print(info.ws_url)  # ws://127.0.0.1:<port>/api/ws?token=...
```

Connect with any WebSocket JSON-RPC client. Protocol reference:
`website/docs/developer-guide/desktop-gateway-protocol.md`.

Optional Python RPC helper (no business wrappers):

```python
import asyncio
from hermes_runtime import HermesRuntime, GatewayRpcClient

async def main():
    with HermesRuntime() as rt:
        info = rt.start()
        async with GatewayRpcClient() as rpc:
            await rpc.connect(info.ws_url)
            ready = asyncio.get_event_loop().create_future()
            rpc.on_event(lambda p: ready.set_result(p) if p.get("type") == "gateway.ready" else None)
            await asyncio.wait_for(ready, timeout=60)

asyncio.run(main())
```

## CLI entrypoint

```bash
xhermes serve --host 127.0.0.1 --port 0
```

Emits `HERMES_BACKEND_READY port=<n>` on stdout when the gateway is listening.

UI commands (`xhermes dashboard`, `xhermes desktop`, `xhermes --tui`) are disabled
in the headless wheel distribution.

## Configuration

- Config: `~/.xhermes/config.yaml`
- Secrets: `~/.xhermes/.env` (API keys only)
- Profiles: `xhermes --profile <name>` or separate `HERMES_HOME`

## Build (maintainers)

```bash
make dist-wheel          # online pip wheel
make dist-offline        # frozen venv + vendored wheels (offline)
```

See [headless-wheel-offline.md](./headless-wheel-offline.md) for frozen/vendored bundle usage.

```bash
HERMES_HEADLESS_WHEEL_BUILD=1 scripts/build_headless_wheel.sh
```
