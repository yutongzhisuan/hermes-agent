---
sidebar_position: 9
title: "Import from Other Agents"
description: "One-command import of a Claude Code (~/.claude) or OpenAI Codex CLI (~/.codex) setup into XHermes — instructions, allowlists, MCP servers, skills, and memories."
---

# Import from Other Agents

`xhermes import-agent` imports your existing **Claude Code** or **OpenAI Codex CLI** setup into XHermes with one command. It follows the same preview-first pattern as [`xhermes claw migrate`](../guides/migrate-from-openclaw.md): you always see a per-item plan before anything is written, and `--dry-run` never touches disk.

```bash
xhermes import-agent                    # auto-detect ~/.claude or ~/.codex
xhermes import-agent claude-code        # import from ~/.claude
xhermes import-agent codex              # import from ~/.codex
xhermes import-agent claude-code --dry-run          # preview only
xhermes import-agent codex --source /path/to/.codex # custom location
xhermes import-agent claude-code --overwrite --yes  # replace conflicts, skip prompts
```

## What gets imported

### Claude Code (`~/.claude`)

| Claude Code | XHermes |
|---|---|
| `CLAUDE.md` (global instructions) | Memory entries in `~/.xhermes/memories/MEMORY.md` |
| `settings.json` → `permissions.allow` (`Bash(...)` rules) | `command_allowlist` in `config.yaml` |
| `settings.json` → `permissions.deny` (`Bash(...)` rules) | `approvals.deny` in `config.yaml` |
| `mcpServers` (from `~/.claude.json` and `settings.json`) | `mcp_servers` in `config.yaml` |
| `skills/<name>/` (dirs with `SKILL.md`) | `~/.xhermes/skills/claude-code-imports/<name>/` |
| `commands/*.md` (slash commands) | Skipped with a note — convert them into skills |

Claude's `Bash(npm run test:*)` prefix rules become `npm run test*` globs. Non-`Bash` permission rules (`Read(...)`, `WebFetch`, ...) gate Claude-specific tools and are reported as unmapped rather than imported.

### Codex CLI (`~/.codex`)

| Codex CLI | XHermes |
|---|---|
| `AGENTS.md` (global instructions) | Memory entries in `~/.xhermes/memories/MEMORY.md` |
| `config.toml` → `[mcp_servers.*]` | `mcp_servers` in `config.yaml` |
| `memories/*.md` | Memory entries in `~/.xhermes/memories/MEMORY.md` |
| `skills/<name>/` (dirs with `SKILL.md`) | `~/.xhermes/skills/codex-imports/<name>/` |

## What is never imported

**API keys and credentials.** Credential files (`~/.claude/.credentials.json`, `~/.codex/auth.json`) are never read, and MCP server environment variables or headers with secret-looking names (`*_TOKEN`, `*_API_KEY`, `Authorization`, ...) are stripped and listed in the report so you can re-add them deliberately. Run `xhermes setup` to configure providers, or add secrets to `~/.xhermes/.env`.

## Behavior notes

- **Preview first, always.** The command prints the full plan before applying; in non-interactive sessions it stops at the preview unless you pass `--yes`.
- **Merges, not replaces.** Memory entries are deduplicated against your existing `MEMORY.md`; allowlist/denylist patterns merge with what's already in `config.yaml`.
- **Conflicts are skipped by default.** An MCP server or skill that already exists in XHermes is reported as a conflict; pass `--overwrite` to replace it.
- **Malformed files don't abort the run.** A broken `settings.json` or `config.toml` becomes a per-item error in the report while everything else still imports.
- Coming from OpenClaw instead? Use [`xhermes claw migrate`](../guides/migrate-from-openclaw.md).
