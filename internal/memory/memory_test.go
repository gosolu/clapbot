package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react"
)

func TestMemoryManagerAndTools(t *testing.T) {
	tmpDir := t.TempDir()
	if err := config.EnsureLayout(tmpDir); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	mgr := NewManager(tmpDir, st)

	// Verify ComposeSystemContext
	sysCtx := mgr.ComposeSystemContext(tmpDir, "cli", "test_session")
	if !strings.Contains(sysCtx, "Clapbot") || !strings.Contains(sysCtx, "Runtime Context") {
		t.Fatalf("unexpected ComposeSystemContext: %s", sysCtx)
	}

	// Verify Save and Read
	msg, err := mgr.Save(ctx, "USER", "- Loves writing Go and TOML", "append")
	if err != nil {
		t.Fatalf("Save USER: %v", err)
	}
	if !strings.Contains(msg, "Saved memory to USER.md") {
		t.Fatalf("unexpected save message: %s", msg)
	}

	readUser, err := mgr.Read("USER")
	if err != nil {
		t.Fatalf("Read USER: %v", err)
	}
	if !strings.Contains(readUser, "Loves writing Go and TOML") {
		t.Fatalf("expected saved content, got: %s", readUser)
	}

	// Save a topic note under memory/<topic>.md
	msg, err = mgr.Save(ctx, "project-notes", "Clapbot uses pure Go and TOML config.", "overwrite")
	if err != nil {
		t.Fatalf("Save topic: %v", err)
	}
	if !strings.Contains(msg, "memory/project-notes.md") {
		t.Fatalf("unexpected save topic message: %s", msg)
	}

	readTopic, err := mgr.Read("project-notes")
	if err != nil || !strings.Contains(readTopic, "Clapbot uses pure Go") {
		t.Fatalf("Read topic error: %v, got %q", err, readTopic)
	}

	// Test Memory Tools
	tools := mgr.Tools()
	var readTool, saveTool, searchTool react.Tool
	for _, tool := range tools {
		switch tool.Spec().Name {
		case "memory_read":
			readTool = tool
		case "memory_save":
			saveTool = tool
		case "memory_search":
			searchTool = tool
		}
	}
	if readTool == nil || saveTool == nil || searchTool == nil {
		t.Fatalf("expected all 3 memory tools")
	}

	// Call memory_save tool
	saveArg, _ := json.Marshal(map[string]string{
		"target":  "golang-tips",
		"content": "Use CGO_ENABLED=0 for portable binaries.",
	})
	res, err := saveTool.Call(ctx, saveArg)
	if err != nil || res.IsError {
		t.Fatalf("memory_save tool error: %v, res: %v", err, res)
	}

	// Call memory_search tool
	searchArg, _ := json.Marshal(map[string]string{
		"query": "CGO_ENABLED",
	})
	res, err = searchTool.Call(ctx, searchArg)
	if err != nil || res.IsError || !strings.Contains(res.Text(), "CGO_ENABLED") {
		t.Fatalf("memory_search tool failed: %v, res: %v", err, res)
	}

	// Verify topic listing in ComposeSystemContext
	sysCtxAfter := mgr.ComposeSystemContext(tmpDir, "cli", "test_session")
	if !strings.Contains(sysCtxAfter, "golang-tips") || !strings.Contains(sysCtxAfter, "project-notes") {
		t.Fatalf("expected available topics listed in system context, got: %s", sysCtxAfter)
	}

	// Verify file was written to disk
	if _, err := os.Stat(filepath.Join(tmpDir, "memory", "golang-tips.md")); err != nil {
		t.Fatalf("expected golang-tips.md on disk: %v", err)
	}
}
