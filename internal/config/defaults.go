package config

const DefaultSoulMD = `# SOUL.md — Agent Identity & Operating Principles

You are **Clapbot**, a personal, self-hosted AI assistant running on the user's machine.
You are powered by the gosolu/react agent engine and connected across CLI, Telegram, and Feishu.

## Core Principles
1. **Be Direct & Action-Oriented**: Execute tasks using your filesystem, shell, web, memory, and scheduling tools rather than guessing or giving generic advice.
2. **Respect Context & Memory**:
   - Read USER.md and MEMORY.md (injected in your system prompt) to honor user preferences and past context.
   - When the user asks you to remember a preference, fact, or project note, use your memory tools (` + "`memory_save`" + `, ` + "`memory_read`" + `, ` + "`memory_search`" + `) to persist it immediately.
3. **Proactive & Reliable**:
   - Use ` + "`schedule_create`" + ` when asked to set reminders, timers, or recurring cron tasks.
   - Delegate self-contained research or coding sub-tasks to sub-agents when appropriate.
4. **Channel Awareness**:
   - Keep responses concise and well-formatted for the current channel (CLI, Telegram, or Feishu).
`

const DefaultUserMD = `# USER.md — User Profile & Preferences

- Preferred language: match the user's message language.
- Communication style: concise, technical, and direct.
`

const DefaultMemoryMD = `# MEMORY.md — Long-Term Memory Index

Durable facts, decisions, and references saved by Clapbot across sessions.
`

const DefaultConfigTOML = `# Clapbot Configuration (~/.clapbot/config.toml)
default_profile = "default"
workspace = "~/.clapbot/workspace"
context_budget = 64000
gateway_addr = "127.0.0.1:8765"
gateway_token = ""

[profiles.default]
provider = "openai" # openai | anthropic | deepseek | gemini | opencode
model = "gpt-4o"
api_key = "${OPENAI_API_KEY}"
base_url = "" # Optional custom endpoint

[profiles.fast]
provider = "openai"
model = "gpt-4o-mini"
api_key = "${OPENAI_API_KEY}"

[profiles.reasoner]
provider = "deepseek"
model = "deepseek-reasoner"
api_key = "${DEEPSEEK_API_KEY}"

[profiles.anthropic]
provider = "anthropic"
model = "claude-sonnet-4-5"
api_key = "${ANTHROPIC_API_KEY}"

[profiles.gemini]
provider = "gemini"
model = "gemini-3.8-flash"
api_key = "${GEMINI_API_KEY}"

[permissions]
mode = "allow_all" # allow_all | rules
deny = []          # Optional tool deny patterns, e.g. ["shell(rm -rf *)"]

[channels.telegram]
enabled = false
bot_token = "${TELEGRAM_BOT_TOKEN}"
allowed_users = [] # Telegram numeric user IDs or usernames
stream_mode = "edit" # edit | final

[channels.feishu]
enabled = false
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
allowed_users = [] # Feishu open_id / user_id allowlist (empty = allow all in private bot)
stream_mode = "edit" # edit | final

[web]
search_provider = "duckduckgo" # duckduckgo | brave | tavily | deepseek
search_api_key = ""
fetch_provider = ""            # direct | deepseek (defaults to deepseek when search_provider = "deepseek")
fetch_api_key = ""

[[subagents]]
name = "researcher"
description = "Research sub-agent for web search, reading documentation, and summarizing findings."
profile = "default"

[[subagents]]
name = "coder"
description = "Coding sub-agent for inspecting, writing, and testing code in the workspace."
profile = "default"

[mcp_servers]
# [mcp_servers.example]
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-everything"]
# env = {}
# url = "" # For HTTP/SSE MCP servers
`

const DefaultConfigYAML = `# Clapbot Configuration (~/.clapbot/config.yaml)
default_profile: default
workspace: ~/.clapbot/workspace
context_budget: 64000
gateway_addr: 127.0.0.1:8765
gateway_token: ""

profiles:
  default:
    provider: openai           # openai | anthropic | deepseek | gemini | opencode
    model: gpt-4o
    api_key: ${OPENAI_API_KEY}
    base_url: ""               # Optional custom endpoint
  fast:
    provider: openai
    model: gpt-4o-mini
    api_key: ${OPENAI_API_KEY}
  reasoner:
    provider: deepseek
    model: deepseek-reasoner
    api_key: ${DEEPSEEK_API_KEY}
  anthropic:
    provider: anthropic
    model: claude-sonnet-4-5
    api_key: ${ANTHROPIC_API_KEY}
  gemini:
    provider: gemini
    model: gemini-3.8-flash
    api_key: ${GEMINI_API_KEY}

permissions:
  mode: allow_all              # allow_all | rules
  deny: []                     # Optional tool deny patterns, e.g. ["shell(rm -rf *)"]

channels:
  telegram:
    enabled: false
    bot_token: ${TELEGRAM_BOT_TOKEN}
    allowed_users: []          # Telegram numeric user IDs or usernames
    stream_mode: edit          # edit | final
  feishu:
    enabled: false
    app_id: ${FEISHU_APP_ID}
    app_secret: ${FEISHU_APP_SECRET}
    allowed_users: []          # Feishu open_id / user_id allowlist (empty = allow all in private bot)
    stream_mode: edit          # edit | final

web:
  search_provider: duckduckgo  # duckduckgo | brave | tavily | deepseek
  search_api_key: ""
  fetch_provider: ""           # direct | deepseek
  fetch_api_key: ""

subagents:
  - name: researcher
    description: "Research sub-agent for web search, reading documentation, and summarizing findings."
    profile: default
  - name: coder
    description: "Coding sub-agent for inspecting, writing, and testing code in the workspace."
    profile: default

mcp_servers: {}
`
