# XHermes CLI Reference

Live sources when anything looks stale: `xhermes --help`, `xhermes <command> --help`,
https://xhermes-agent.nousresearch.com/docs/reference/cli-commands

### Global Flags

```
xhermes [flags] [command]        (no subcommand = interactive chat)

  --version, -V             Show version
  -z, --oneshot PROMPT      One-shot: print ONLY the final response (for scripts/pipes)
  -m MODEL  --provider P    Model/provider override for this invocation
  -t, --toolsets LIST       Comma-separated toolsets for this invocation
  --resume, -r SESSION      Resume session by ID or title
  --continue, -c [NAME]     Resume by name, or most recent session
  --worktree, -w            Isolated git worktree mode (parallel agents)
  --skills, -s SKILL        Preload skills (comma-separate or repeat)
  --profile, -p NAME        Use a named profile
  --yolo                    Skip dangerous command approval
  --tui / --cli             Force the Ink TUI / classic REPL
  --ignore-rules            Skip AGENTS.md/SOUL.md/memory/skill injection
  --safe-mode               Disable ALL customizations (troubleshooting)
  --pass-session-id         Include session ID in system prompt
```

### Chat

```
xhermes chat [flags]
  -q, --query TEXT          Single query, non-interactive
  --image PATH              Attach a local image to a single query
  -Q, --quiet               Suppress banner, spinner, tool previews
  --checkpoints             Enable filesystem checkpoints (/rollback)
  --max-turns N             Cap tool-calling iterations
  --source TAG              Session source tag (default: cli)
```
(plus the global flags above)

### Configuration

```
xhermes setup [section]      Wizard (model|tts|terminal|gateway|tools|agent)
xhermes model                Interactive model/provider picker
xhermes fallback [add|remove|list]  Fallback provider chain
xhermes config [show|edit|get|set|unset|path|env-path|check|migrate]
xhermes login / logout       OAuth sign-in / clear stored auth
xhermes doctor [--fix]       Check dependencies and config
xhermes status [--all]       Component status
```

### Tools & Skills

```
xhermes tools [list|enable NAME|disable NAME]   Per-platform toolsets (curses UI with no args)

xhermes skills list|browse|search QUERY|inspect ID
xhermes skills install ID    Hub identifier OR a direct https://…/SKILL.md URL
xhermes skills config        Enable/disable skills per platform
xhermes skills check|update|uninstall|publish PATH
xhermes skills tap add REPO  Add a GitHub repo as a skill source
xhermes bundles              Skill bundles (one /<name> alias loads several skills)
```

### MCP Servers

```
xhermes mcp add NAME (--url or --command) | remove | list | test NAME
xhermes mcp catalog | install NAME     Curated catalog install
xhermes mcp configure NAME             Toggle tool selection
xhermes mcp serve                      Run XHermes as an MCP server
```
Details (transport, tool discovery, catalog): `references/native-mcp.md`.

### Gateway (Messaging Platforms)

```
xhermes gateway run|install|start|stop|restart|status|setup
```

20+ platforms: Telegram, Discord, Slack, WhatsApp (Baileys + Business Cloud API), iMessage (Photon — `xhermes photon setup`), Signal, Email, SMS, Matrix, Mattermost, Teams, LINE, SimpleX, ntfy, Google Chat, Home Assistant, DingTalk, Feishu, WeCom, Weixin, API Server, Webhooks. Open WebUI connects via the API Server adapter. Most adapters ship under `plugins/platforms/`.
Docs: https://xhermes-agent.nousresearch.com/docs/user-guide/messaging/

### Sessions

```
xhermes sessions list|browse|rename ID TITLE|delete ID|export OUT|prune|stats
```

### Cron / Webhooks

```
xhermes cron list|create SCHED|edit ID|pause|resume|run ID|remove|status
    Schedules: '30m', 'every 2h', '0 9 * * *', ISO timestamp
xhermes webhook subscribe NAME|list|remove NAME|test NAME
```
Webhook payloads/routes: `references/webhooks.md`.

### Profiles

```
xhermes profile list|create NAME (--clone|--clone-all|--clone-from)|use|show|delete
xhermes profile rename A B | alias NAME | export NAME | import FILE
```

### Credentials & Pools

```
xhermes auth                 Interactive credential manager
xhermes auth add [PROVIDER]  Add OAuth or API-key credential (nous, openai-codex, qwen-oauth, …)
xhermes auth list|remove P IDX|reset PROVIDER|status
```
Multiple credentials per provider form a pool that rotates automatically and skips exhausted keys.

### Other

```
xhermes desktop / gui        Native desktop app
xhermes dashboard            Web admin panel + embedded chat (--stop / --status)
xhermes proxy                OpenAI-compatible local proxy backed by an OAuth provider
xhermes portal               Quick setup / sign in via Nous Portal
xhermes kanban <verb>        Multi-agent work-queue board
xhermes project              Named multi-folder workspaces
xhermes skin list|use|set    Switch/tweak skins (see references/themes.md)
xhermes pets <verb>          Pet mascots (see references/petdex.md)
xhermes memory setup|status|off|reset   Memory provider
xhermes secrets bitwarden|onepassword   External secret stores
xhermes moa                  Mixture-of-Agents slots
xhermes hooks / security / backup / import / checkpoints / console
xhermes logs [-f] [errors]   View agent/error logs
xhermes send                 One-off message through a gateway platform
xhermes pairing / plugins / insights / journey / computer-use
xhermes acp                  ACP server (IDE integration)
xhermes completion bash|zsh|fish
xhermes update / uninstall / claw migrate
```

Plugin- and provider-supplied subcommands (e.g. `xhermes photon setup`) only appear once their plugin is installed/active.

### Where to Find Things

| Looking for... | Location |
|---|---|
| Config options | `xhermes config edit` · [Configuration docs](https://xhermes-agent.nousresearch.com/docs/user-guide/configuration) |
| Tools / toolsets | `xhermes tools list` · [Tools reference](https://xhermes-agent.nousresearch.com/docs/reference/tools-reference) |
| Skills catalog | `xhermes skills browse` · [Skills catalog](https://xhermes-agent.nousresearch.com/docs/reference/skills-catalog) |
| Provider setup | `xhermes model` · [Providers guide](https://xhermes-agent.nousresearch.com/docs/integrations/providers) |
| Env variables | `xhermes config env-path` · [Env vars reference](https://xhermes-agent.nousresearch.com/docs/reference/environment-variables) |
| Gateway logs | `~/.xhermes/logs/gateway.log` (or `xhermes logs`) |
| Sessions | `xhermes sessions browse` (reads state.db) |
