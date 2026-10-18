package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react/provider/mock"
)

func TestEngineTurnAndCommands(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	router, err := provider.NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	mockProv := mock.New(
		mock.ToolCalls("", mock.Call("memory_save", map[string]any{
			"target":  "USER",
			"content": "- Favorite language: Go",
			"mode":    "append",
		})),
		mock.Text("Saved your preference!"),
	)
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock-1"}, mockProv)

	eng, err := NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	res, cmdRes, err := eng.RunTurn(ctx, TurnRequest{
		SessionID: "test_s1",
		Channel:   "cli",
		Input:     "Remember I love Go",
	})
	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if cmdRes.Handled {
		t.Fatalf("expected non-command turn")
	}
	if res.Output != "Saved your preference!" {
		t.Fatalf("unexpected output: %q", res.Output)
	}

	userMD, err := eng.Memory().Read("USER")
	if err != nil {
		t.Fatalf("Memory.Read(USER): %v", err)
	}
	if !strings.Contains(userMD, "Favorite language: Go") {
		t.Fatalf("expected USER.md to contain saved preference, got: %s", userMD)
	}

	// Test slash command /status
	_, statusCmd, err := eng.RunTurn(ctx, TurnRequest{
		SessionID: "test_s1",
		Channel:   "cli",
		Input:     "/status",
	})
	if err != nil || !statusCmd.Handled {
		t.Fatalf("/status command failed: %v", err)
	}
	if !strings.Contains(statusCmd.Output, "test_s1") {
		t.Fatalf("expected /status output to mention test_s1, got: %s", statusCmd.Output)
	}
}
