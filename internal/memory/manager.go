package memory

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gosolu/clapbot/internal/store"
)

// Manager manages Markdown persona/memory files (SOUL.md, USER.md, MEMORY.md, memory/*.md)
// and synchronizes them with the SQLite FTS5 index.
type Manager struct {
	homeDir string
	store   *store.SQLiteStore
	mu      sync.RWMutex
}

// NewManager creates a memory Manager rooted at homeDir (~/.clapbot).
func NewManager(homeDir string, st *store.SQLiteStore) *Manager {
	m := &Manager{
		homeDir: homeDir,
		store:   st,
	}
	_ = m.SyncIndex(context.Background())
	return m
}

// SyncIndex indexes USER.md, MEMORY.md, and all memory/*.md files into SQLite FTS5.
func (m *Manager) SyncIndex(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, name := range []string{"USER.md", "MEMORY.md"} {
		p := filepath.Join(m.homeDir, name)
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			topic := strings.ToLower(strings.TrimSuffix(name, ".md"))
			_ = m.store.IndexMemoryEntry(ctx, topic, string(b), name)
		}
	}

	memDir := filepath.Join(m.homeDir, "memory")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(memDir, e.Name())
		if b, err := os.ReadFile(p); err == nil {
			topic := strings.TrimSuffix(e.Name(), ".md")
			_ = m.store.IndexMemoryEntry(ctx, topic, string(b), "memory/"+e.Name())
		}
	}
	return nil
}

// ComposeSystemContext reads SOUL.md, USER.md, MEMORY.md, and optional workspace CLAPBOT.md/AGENTS.md
// to assemble the dynamic system prompt context.
func (m *Manager) ComposeSystemContext(workspaceDir, channel, sessionID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sections []string

	if b, err := os.ReadFile(filepath.Join(m.homeDir, "SOUL.md")); err == nil && strings.TrimSpace(string(b)) != "" {
		sections = append(sections, strings.TrimSpace(string(b)))
	}
	if b, err := os.ReadFile(filepath.Join(m.homeDir, "USER.md")); err == nil && strings.TrimSpace(string(b)) != "" {
		sections = append(sections, strings.TrimSpace(string(b)))
	}
	if b, err := os.ReadFile(filepath.Join(m.homeDir, "MEMORY.md")); err == nil && strings.TrimSpace(string(b)) != "" {
		sections = append(sections, strings.TrimSpace(string(b)))
	}

	// List available topic files under ~/.clapbot/memory/
	if topics := m.listTopicsLocked(); len(topics) > 0 {
		sections = append(sections, fmt.Sprintf("## Available Memory Topics (`memory_read`)\n%s", strings.Join(topics, ", ")))
	}

	if workspaceDir != "" {
		for _, wf := range []string{"CLAPBOT.md", "AGENTS.md"} {
			p := filepath.Join(workspaceDir, wf)
			if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) != "" {
				sections = append(sections, fmt.Sprintf("## Workspace Instructions (%s)\n%s", wf, strings.TrimSpace(string(b))))
				break
			}
		}
	}

	envInfo := fmt.Sprintf("## Runtime Context\n- Current Time: %s\n- Active Workspace: %s\n- Channel: %s\n- Session ID: %s",
		time.Now().Format(time.RFC3339), workspaceDir, channel, sessionID)
	sections = append(sections, envInfo)

	return strings.Join(sections, "\n\n---\n\n")
}

func (m *Manager) listTopicsLocked() []string {
	entries, err := os.ReadDir(filepath.Join(m.homeDir, "memory"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(out)
	return out
}

// Read reads a target memory file ("USER", "MEMORY", "SOUL", or a topic under memory/<topic>.md).
func (m *Manager) Read(target string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	path, _, err := m.resolveTarget(target)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "(empty)", nil
		}
		return "", err
	}
	return string(b), nil
}

// Save writes or appends content to USER.md, MEMORY.md, SOUL.md, or memory/<topic>.md, and indexes it in FTS5.
func (m *Manager) Save(ctx context.Context, target, content, mode string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	path, relSource, err := m.resolveTarget(target)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}

	finalContent := content
	if strings.EqualFold(mode, "append") {
		existing, _ := os.ReadFile(path)
		if len(existing) > 0 {
			sep := "\n"
			if strings.HasSuffix(string(existing), "\n") {
				sep = ""
			}
			finalContent = string(existing) + sep + content + "\n"
		} else {
			finalContent = content + "\n"
		}
	}

	if err := os.WriteFile(path, []byte(finalContent), 0644); err != nil {
		slog.ErrorContext(ctx, "failed to write memory file", "path", path, "target", target, "error", err)
		return "", err
	}

	if m.store != nil {
		_ = m.store.IndexMemoryEntry(ctx, sanitizeTopicName(target), finalContent, relSource)
	}
	slog.InfoContext(ctx, "saved memory", "source", relSource, "bytes", len(finalContent), "mode", mode)
	return fmt.Sprintf("Saved memory to %s (%d bytes)", relSource, len(finalContent)), nil
}

func (m *Manager) resolveTarget(target string) (string, string, error) {
	t := strings.TrimSpace(target)
	t = strings.TrimSuffix(t, ".md")
	switch strings.ToUpper(t) {
	case "USER":
		return filepath.Join(m.homeDir, "USER.md"), "USER.md", nil
	case "MEMORY", "":
		return filepath.Join(m.homeDir, "MEMORY.md"), "MEMORY.md", nil
	case "SOUL":
		return filepath.Join(m.homeDir, "SOUL.md"), "SOUL.md", nil
	default:
		safe := sanitizeTopicName(t)
		if safe == "" {
			return "", "", fmt.Errorf("invalid memory topic %q", target)
		}
		rel := filepath.Join("memory", safe+".md")
		return filepath.Join(m.homeDir, rel), rel, nil
	}
}

func sanitizeTopicName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '/' || r == '.' {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
