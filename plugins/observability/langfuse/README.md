# Langfuse Observability Plugin

This plugin ships bundled with XHermes but is **opt-in** — it only loads when
you explicitly enable it.

## Enable

Pick one:

```bash
# Interactive: walks you through credentials + SDK install + enable
xhermes tools  # → Langfuse Observability

# Manual
pip install langfuse
xhermes plugins enable observability/langfuse
```

## Required credentials

Set these in `~/.xhermes/.env` (or via `xhermes tools`):

```bash
XHERMES_LANGFUSE_PUBLIC_KEY=pk-lf-...
XHERMES_LANGFUSE_SECRET_KEY=sk-lf-...
XHERMES_LANGFUSE_BASE_URL=https://cloud.langfuse.com   # or your self-hosted URL
```

Without the SDK or credentials the hooks no-op silently — the plugin fails
open.

## Verify

```bash
xhermes plugins list                 # observability/langfuse should show "enabled"
xhermes chat -q "hello"              # then check Langfuse for a "XHermes turn" trace
```

## Optional tuning

```bash
XHERMES_LANGFUSE_ENV=production       # environment tag
XHERMES_LANGFUSE_RELEASE=v1.0.0       # release tag
XHERMES_LANGFUSE_SAMPLE_RATE=0.5      # sample 50% of traces
XHERMES_LANGFUSE_MAX_CHARS=12000      # max chars per field (default: 12000)
XHERMES_LANGFUSE_DEBUG=true           # verbose plugin logging
```

## Disable

```bash
xhermes plugins disable observability/langfuse
```
