package feishu

import (
	"context"
	"fmt"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react/provider/mock"
)

func TestFeishuInboundSuperAgentTrigger(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	cfg.Mode = "superagent"
	cfg.SuperAgent.AcceptMode = "auto"
	cfg.Channels.Feishu.AllowedUsers = []string{"ou_feishu_user"}

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

	bot := New(cfg.Channels.Feishu, tmp, eng)

	rawEvent := []byte(fmt.Sprintf(`{
		"header": {
			"event_id": "ev_001",
			"event_type": "im.message.receive_v1"
		},
		"event": {
			"sender": {
				"sender_id": {
					"open_id": "ou_feishu_user",
					"user_id": "user_feishu_user"
				}
			},
			"message": {
				"message_id": "om_123456",
				"chat_id": "oc_chat_999",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"Fix styling in mobile view\"}"
			}
		}
	}`))

	bot.handleEventJSON(ctx, rawEvent)

	// Verify trigger in superagent
	tr, err := eng.SuperAgent().Decide(ctx, "feishu:om_123456", "accept", "Test accept", nil)
	if err != nil {
		t.Fatalf("failed to decide trigger: %v", err)
	}
	if tr == nil || tr.TicketID == "" {
		t.Fatalf("expected ticket created for feishu trigger, got: %+v", tr)
	}
}
