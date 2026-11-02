package telegram

import (
	"context"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react/provider/mock"
)

func TestTelegramInboundSuperAgentTrigger(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	cfg.Mode = "superagent"
	cfg.SuperAgent.AcceptMode = "auto"
	cfg.Channels.Telegram.AllowedUsers = []string{"testuser"}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	router, _ := provider.NewRouter(cfg)
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock"}, mock.New(
		mock.Text("OK"),
	))

	eng, err := runtime.NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if !eng.IsSuperAgent() {
		t.Fatalf("expected engine in superagent mode")
	}

	bot := New(cfg.Channels.Telegram, tmp, eng)

	msg := &tgMessage{
		MessageID: 1001,
		From: &tgUser{
			ID:       9999,
			Username: "testuser",
		},
		Chat: tgChat{
			ID:   12345,
			Type: "private",
		},
		Text: "Please refactor the database connector",
	}

	bot.handleInbound(ctx, msg)

	// Verify trigger in SQLite store
	tr, err := eng.SuperAgent().Decide(ctx, "telegram:12345:1001", "accept", "Test accept", nil)
	if err != nil {
		t.Fatalf("failed to decide trigger: %v", err)
	}
	if tr == nil || tr.TicketID == "" {
		t.Fatalf("expected ticket created for telegram trigger, got: %+v", tr)
	}
}
