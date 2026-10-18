package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/gosolu/react"
)

type memoryReadInput struct {
	Target string `json:"target" desc:"Memory target to read: 'USER', 'MEMORY', 'SOUL', or a topic name under memory/ (e.g. 'project-notes')."`
}

type memorySaveInput struct {
	Target  string `json:"target" desc:"Memory target to write: 'USER' (user preferences/profile), 'MEMORY' (core long-term facts), 'SOUL' (persona), or a topic name (e.g. 'golang-tips')."`
	Content string `json:"content" desc:"Markdown content to save."`
	Mode    string `json:"mode,omitempty" enum:"append,overwrite" desc:"Write mode: 'append' (default) adds to existing file, 'overwrite' replaces the entire file."`
}

type memorySearchInput struct {
	Query string `json:"query" desc:"Keywords to search across long-term memory notes and past conversation history."`
	Limit int    `json:"limit,omitempty" desc:"Maximum number of search results to return (default 10)."`
}

// Tools returns the react.Tool suite for reading, saving, and searching long-term memory.
func (m *Manager) Tools() []react.Tool {
	readTool := react.NewTool(
		"memory_read",
		"Read durable agent memory from USER.md, MEMORY.md, SOUL.md, or a topic note in ~/.clapbot/memory/.",
		func(ctx context.Context, in memoryReadInput) (string, error) {
			return m.Read(in.Target)
		},
		react.ReadOnly(),
	)

	saveTool := react.NewTool(
		"memory_save",
		"Save or append durable user preferences, facts, or topic notes to USER.md, MEMORY.md, SOUL.md, or ~/.clapbot/memory/<topic>.md.",
		func(ctx context.Context, in memorySaveInput) (string, error) {
			mode := in.Mode
			if mode == "" {
				mode = "append"
			}
			return m.Save(ctx, in.Target, in.Content, mode)
		},
	)

	searchTool := react.NewTool(
		"memory_search",
		"Search long-term memory files and past conversation transcripts using full-text search.",
		func(ctx context.Context, in memorySearchInput) (string, error) {
			if m.store == nil {
				return "Memory search index not available.", nil
			}
			hits, err := m.store.SearchMemory(ctx, in.Query, in.Limit)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "No matching memory entries found.", nil
			}
			var b strings.Builder
			for i, h := range hits {
				fmt.Fprintf(&b, "%d. [%s / %s] %s\n", i+1, h.Source, h.Topic, h.Snippet)
			}
			return b.String(), nil
		},
		react.ReadOnly(),
	)

	return []react.Tool{readTool, saveTool, searchTool}
}
