---
name: skill-creator
description: Create, edit, and validate custom progressive-disclosure SKILL.md skills in ~/.clapbot/skills/ or the active workspace.
---

# Skill Creator Guide

Use this skill when the user asks you to create a reusable workflow, SOP, or custom skill for Clapbot.

## Skill Directory Layout
Each skill lives in its own folder under `~/.clapbot/skills/<skill-name>/` (global) or `<workspace>/.agents/skills/<skill-name>/` (project-local):

```text
<skill-name>/
├── SKILL.md          # Required: YAML frontmatter + Markdown instructions
├── scripts/          # Optional: helper scripts
└── references/       # Optional: reference docs loaded via read_skill_file
```

## Frontmatter Rules
- `name`: 1-64 chars, lowercase alphanumeric and single hyphens (`a-z0-9-`), matching the directory name.
- `description`: Clear trigger description (1-1024 chars) explaining what the skill does and when to activate it.
- `allowed-tools`: Optional space-separated tool patterns pre-approved when the skill is active.
