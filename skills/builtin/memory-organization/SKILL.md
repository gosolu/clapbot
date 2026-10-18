---
name: memory-organization
description: Organize, curate, and structure long-term user preferences (USER.md), core facts (MEMORY.md), and topic notes (memory/*.md).
---

# Memory Organization Guide

When the user asks you to remember preferences, project context, or recurring facts:

1. **User Preferences (`USER.md`)**:
   - Store communication style, preferred languages, coding conventions, and personal workflow rules in `USER` via `memory_save`.
2. **Core Index (`MEMORY.md`)**:
   - Keep `MEMORY.md` concise as a high-level summary of important facts and pointers to topic files.
3. **Topic Files (`memory/<topic>.md`)**:
   - For detailed project notes, credentials references, or domain knowledge, save to a dedicated topic name (e.g. `memory_save` with `target: "project-x"`) so it is indexed in FTS5 and listed under Available Memory Topics without bloating every prompt.
