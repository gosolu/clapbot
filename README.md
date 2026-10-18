# 👏 Clapbot

A self-hosted, single-binary personal AI assistant and multi-channel agent gateway written in Go (`CGO_ENABLED=0`), built on top of [`github.com/gosolu/react`](https://github.com/gosolu/react).

Inspired by **OpenClaw**, **Nanobot**, and **ZeroClaw**.

---

## Features

- **Powered by `gosolu/react`:** Unified ReAct loop, streaming events, progressive disclosure `SKILL.md` skills, lifecycle hooks, context summarization, and `AsTool` sub-agents.
- **Single Pure-Go Binary (`CGO_ENABLED=0`):** Cross-compiles cleanly with zero CGO dependencies.
- **Unified Gateway & Message Bus:**
  - **Interactive CLI (`clapbot chat`):** Lightweight native-scrollback REPL that automatically attaches to the running local Gateway daemon over WebSocket or runs embedded in-process, with interactive `huh` pickers for `/sessions` and `/model`.
  - **Telegram Bot Channel:** Pure HTTP long-polling (`getUpdates`), sender allowlist, rate-limit-safe live message editing (`edit` / `final` stream modes), and photo/document transfer.
  - **Feishu / Lark Bot Channel:** WebSocket long-connection mode (no public IP or webhook server required) with pure-Go protobuf frame decoding, live message editing, and file transfer.
- **Hybrid Memory Architecture:**
  - **SQLite (`~/.clapbot/clapbot.db`):** Implements `react.Store` for conversation history, session/workspace bindings, cron schedules, and **FTS5** full-text search.
  - **Human-Editable Markdown (`~/.clapbot/{SOUL,USER,MEMORY}.md` + `memory/*.md`):** Composed dynamically into the system prompt on every turn and updated via `memory_read`, `memory_save`, and `memory_search` tools.
- **Proactive Cron & One-Shot Timers:** Persistent scheduler with `schedule_create`, `schedule_list`, and `schedule_delete` tools that wake the agent and push results to Telegram, Feishu, or CLI.
- **Built-in Web, Channel & MCP Tools:**
  - `web_fetch` (HTML-to-Markdown) and `web_search` (DuckDuckGo, Brave, Tavily, SearXNG).
  - `channel_send_message` and `channel_send_file` for proactive notifications and file delivery.
  - Native **MCP Client Plugin** (`stdio` and Streamable HTTP JSON-RPC 2.0) bridged via `react.Plugin`.
- **OS Daemon Service Management:** Built-in `launchd` (macOS) and `systemd` (Linux) service installer (`clapbot service install|start|stop|status|logs`).

---

## Quick Start

```bash
# Build pure-Go binary
CGO_ENABLED=0 go build -o clapbot ./cmd/clapbot

# Run interactive onboarding wizard (configures provider, model, Telegram, Feishu)
./clapbot onboard

# Start interactive terminal chat
./clapbot chat

# Run one-shot command
./clapbot run -m "Summarize the files in this directory"

# Start the Gateway daemon + Telegram/Feishu channels + Cron scheduler
./clapbot serve

# Or install and start as a background OS service (macOS launchd / Linux systemd)
./clapbot service install
./clapbot service start
```

---

## Slash Commands (CLI, Telegram & Feishu)

| Command | Description |
| :--- | :--- |
| `/new` | Start a fresh conversation session |
| `/reset` | Clear messages in the current session |
| `/sessions [id]` | List recent sessions (or open interactive picker in CLI) / switch to `id` |
| `/workspace [path]` | Show or change the current session's filesystem workspace |
| `/model [profile]` | Show or switch the active model profile (`default`, `fast`, `reasoner`, etc.) |
| `/compact` | Summarize and compact current conversation history via `react.ContextSummarizer` |
| `/schedules` | List active cron jobs and one-shot timers |
| `/tools` | List all registered tools (including MCP and sub-agent tools) |
| `/skills` | List all loaded `SKILL.md` skills |
| `/status` | Show session, model, workspace, and runtime status |
| `/detach` | Detach CLI from the background Gateway daemon |
| `/exit` | Exit the CLI REPL |
