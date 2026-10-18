# AGENTS.md — Clapbot Developer & Agent Guide

`clapbot` is a self-hosted, single-binary personal AI assistant and multi-channel agent gateway written in Go (`CGO_ENABLED=0`), built on top of [`github.com/gosolu/react`](https://github.com/gosolu/react).

---

## 1. Build, Test & Run Commands

```bash
# Build pure-Go binary (must always compile with CGO_ENABLED=0)
CGO_ENABLED=0 go build -o clapbot ./cmd/clapbot

# Run all unit and integration tests
CGO_ENABLED=0 go test -v ./...

# Run tests for a specific package
CGO_ENABLED=0 go test -v ./internal/runtime/...

# Run interactive onboarding wizard
./clapbot onboard

# Start interactive CLI REPL (attaches to local Gateway if running, or runs in-process)
./clapbot chat

# Run a single prompt non-interactively
./clapbot run -m "Hello!"

# Start the local Gateway (HTTP/WS) + Scheduler + Telegram & Feishu channels
./clapbot serve
```

---

## 2. Core Architectural Rules & Constraints

1. **Pure Go (`CGO_ENABLED=0`)**:
   - Every dependency and feature MUST compile and pass tests with `CGO_ENABLED=0`.
   - SQLite uses `modernc.org/sqlite` (pure Go). Never introduce CGO-based SQLite (`mattn/go-sqlite3`) or C bindings.
2. **Strict Boundary with `github.com/gosolu/react`**:
   - `gosolu/react` is the zero-dependency ReAct core (`Agent`, `Provider`, `Tool`, `Skill`, `Store`, `Hooks`, `Plugin`, `ContextSummarizer`, `PermissionPolicy`).
   - Do not duplicate the ReAct loop, tool executor, or skill parser inside `clapbot`. Instead, extend the agent through `react`'s exported interfaces:
     - Session persistence via `react.Store` (`internal/store/sqlite.go`)
     - Multi-profile model routing via `react.Provider` (`internal/provider/router.go`)
     - External tool ecosystems (MCP) via `react.Plugin` and `react.NewRawTool` (`internal/mcp/plugin.go`)
     - Dynamic system prompt composition (`SOUL.md`, `USER.md`, `MEMORY.md`, `<workspace>/CLAPBOT.md` or `AGENTS.md`) via `react.Hooks.BeforeModel` (`internal/runtime/engine.go`)
     - Per-session filesystem/shell workspace scoping via `react.WithRunTools(...)` (`internal/runtime/engine.go`)
3. **RunContext Propagation**:
   - Channel-aware tools (`schedule_create`, `channel_send_message`, `channel_send_file`) read the active session's metadata from `react.RunContextFrom(ctx)` using keys defined in `internal/tools/cron`:
     - `cron.ContextKeyWorkspace` (`"clapbot.workspace"`)
     - `cron.ContextKeyChannel` (`"clapbot.channel"`)
     - `cron.ContextKeyChatID` (`"clapbot.chat_id"`)

---

## 3. Repository Structure

```text
clapbot/
├── cmd/clapbot/main.go               # CLI entrypoint & subcommands (chat, run, serve, onboard, status, service)
├── internal/
│   ├── config/
│   │   ├── config.go                 # ~/.clapbot/config.toml schema, loader, saver & ${ENV} expansion (with config.yaml compat)
│   │   └── defaults.go               # Default SOUL.md, USER.md, MEMORY.md, and config.toml templates
│   ├── provider/
│   │   └── router.go                 # Multi-profile react.Provider wrapper (openai, anthropic, deepseek, opencode, mock)
│   ├── store/
│   │   ├── sqlite.go                 # SQLite react.Store + sessions, channel_bindings, and schedules tables
│   │   └── fts.go                    # SQLite FTS5 & fallback search across memory entries and session history
│   ├── memory/
│   │   ├── manager.go                # Manages SOUL.md, USER.md, MEMORY.md, memory/*.md & FTS5 sync
│   │   └── tools.go                  # react.Tool: memory_read, memory_save, memory_search
│   ├── runtime/
│   │   ├── engine.go                 # Central Engine wrapping react.Agent, sub-agents, hooks & compaction
│   │   ├── commands.go               # Unified slash commands (/new, /reset, /sessions, /workspace, /model, /compact, etc.)
│   │   └── bus.go                    # Channel registry, proactive message/file sender, and live TurnEvent pub/sub
│   ├── tools/
│   │   ├── web/web.go                # web_fetch (HTML-to-Markdown) & web_search (DuckDuckGo, Brave, Tavily, SearXNG)
│   │   ├── cron/scheduler.go         # Persistent cron & one-shot timer scheduler + schedule_* tools
│   │   └── channel/channel.go        # channel_send_message & channel_send_file tools
│   ├── mcp/
│   │   ├── client.go                 # JSON-RPC 2.0 MCP client (stdio & HTTP transports)
│   │   └── plugin.go                 # Bridges MCP servers into react.Agent as a react.Plugin
│   ├── channels/
│   │   ├── channel.go                # Shared channel turn executor, 1.1s rate-limited live message editor, text chunker
│   │   ├── telegram/telegram.go      # Telegram Bot adapter (HTTP long-polling, allowlist, media download/upload)
│   │   └── feishu/feishu.go          # Feishu/Lark adapter (WebSocket long-connection, pure-Go pbbp2 protobuf codec)
│   ├── gateway/
│   │   ├── server.go                 # Local HTTP + WebSocket server (/health, /api/sessions, /api/profiles, /ws)
│   │   └── client.go                 # Gateway client used by `clapbot chat` to attach to a running daemon
│   ├── cli/
│   │   ├── repl.go                   # Native-scrollback terminal REPL with live streaming & Gateway attach
│   │   ├── onboard.go                # Interactive `charmbracelet/huh` setup wizard (`clapbot onboard`)
│   │   └── picker.go                 # Interactive `huh` pickers for `/sessions` and `/model`
│   └── service/
│       └── service.go                # macOS launchd plist & Linux systemd user unit installer/controller
└── skills/builtin/                   # Embedded default SKILL.md skills (skill-creator, memory-organization)
```

---

## 4. Extension Guidelines

- **Adding a Built-in Tool**:
  - Define typed input structs with `json` and `desc` tags and construct the tool with `react.NewTool(name, description, handler, opts...)`.
  - Mark read-only tools with `react.ReadOnly()`.
  - Register the tool in `runtime.NewEngine` (`internal/runtime/engine.go`).
- **Adding a Chat Channel**:
  - Implement `runtime.ChannelAdapter` (`Name`, `Start`, `Stop`, `SendText`, `SendFile`) and `channels.MessageTransport` (`SendInitial`, `EditMessage`, `SendChunk`, `SendTyping`).
  - Use `channels.IsAllowedUser` to enforce sender allowlists and `channels.ExecuteChannelTurn` to get slash commands, rate-limited live message editing, and message chunking automatically.
  - Register the channel in `runServe` (`cmd/clapbot/main.go`).
- **Adding a Bundled Skill**:
  - Create `skills/builtin/<skill-name>/SKILL.md` with valid `name` (matching the directory name) and `description` YAML frontmatter. It is embedded automatically via `//go:embed *` in `skills/builtin/skills.go`.
