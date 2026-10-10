# Headless Wheel Dependencies

Migration table for the headless pip wheel **single-package** strategy.

## Promoted to core (`dependencies`)

| Former extra | Packages | Notes |
|--------------|----------|-------|
| `web` | fastapi, uvicorn, starlette, python-multipart | Required for `xhermes serve` |
| `mcp` | mcp | MCP client |
| `acp` | agent-client-protocol | Editor ACP integration |
| `messaging` | python-telegram-bot, discord.py, aiohttp, slack-*, qrcode, brotlicffi | Gateway platforms |
| `dingtalk` | dingtalk-stream, alibabacloud-dingtalk | DingTalk adapter |
| `feishu` | lark-oapi | Feishu/Lark adapter |
| `edge-tts` | edge-tts | Default TTS backend |
| `voice` | faster-whisper, sounddevice, numpy | Local STT |
| `wake` | openwakeword, onnxruntime, sherpa-onnx, sentencepiece, pvporcupine, ai-edge-litert (macOS) | Wake word |

Extras above remain as **back-compat aliases** in `pyproject.toml` for `pip install xhermes-agent[extra]`.

## Still lazy / optional

| Extra | Reason |
|-------|--------|
| `matrix` | python-olm has no Windows/macOS wheels |
| `honcho`, `supermemory`, `mem0`, `hindsight` | Opt-in memory backends |
| `anthropic`, `exa`, `firecrawl`, `fal`, … | Provider-specific opt-in backends |
| `google`, `youtube` | Heavy Google API stack; install when skills need it |

## Impact

- Headless wheel installs are larger but need no extra `pip install xhermes-agent[...]` for serve + gateway + MCP + messaging.
- `[all]` no longer references `mcp` or `acp` (already in core).
