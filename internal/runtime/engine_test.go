package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/clapbot/internal/superagent"
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

func TestEngineSuperAgentPolicyCommands(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Mode = "superagent"
	cfg.SuperAgent = config.SuperAgentConfig{
		AcceptMode: "observe",
		Rules: []config.SuperAgentRule{
			{Name: "bug_hunter", Instructions: "identify and report bugs", Enabled: true},
		},
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
	mockProv := mock.New(mock.Text("ok"))
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock-1"}, mockProv)

	eng, err := NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if !eng.IsSuperAgent() {
		t.Fatalf("expected IsSuperAgent to be true")
	}

	// 1. Test /policy
	cmdRes, err := eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/policy",
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/policy command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "observe") || !strings.Contains(cmdRes.Output, "bug_hunter") {
		t.Fatalf("unexpected /policy output: %s", cmdRes.Output)
	}

	// 2. Test /policy mode auto
	cmdRes, err = eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/policy mode auto",
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/policy mode command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "accept mode is now `auto`") {
		t.Fatalf("unexpected /policy mode output: %s", cmdRes.Output)
	}
	if eng.SuperAgent().Policy().Mode() != "auto" {
		t.Fatalf("expected policy mode to be auto, got %s", eng.SuperAgent().Policy().Mode())
	}

	// 3. Test /policy rule bug_hunter disable
	cmdRes, err = eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/policy rule bug_hunter disable",
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/policy rule command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "rule `bug_hunter` is now disabled") {
		t.Fatalf("unexpected /policy rule output: %s", cmdRes.Output)
	}
	if _, found := eng.SuperAgent().Policy().Rule("bug_hunter"); found {
		t.Fatalf("expected bug_hunter rule to be disabled")
	}
}

func TestEngineSuperAgentTicketAndAttentionCommands(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Mode = "superagent"
	cfg.SuperAgent = config.SuperAgentConfig{
		AcceptMode: "auto",
		Rules: []config.SuperAgentRule{
			{Name: "code_fix", Instructions: "fix bugs", Enabled: true},
		},
	}
	cfg.Channels.Telegram.AllowedUsers = []string{"alice"}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	router, err := provider.NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock-1"}, mock.New(mock.Text("ok")))

	eng, err := NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	sa := eng.SuperAgent()

	// 1. Ingest trigger and accept to create ticket
	_, err = sa.Receive(ctx, superagent.Trigger{
		ID:        "telegram:1:200",
		Source:    "telegram",
		Scope:     "chat1",
		Thread:    "telegram:chat1",
		Actor:     superagent.Actor{ID: "alice"},
		Addressed: superagent.AddressDirect,
		Text:      "Fix race condition in store",
		Channel:   superagent.ChannelAddress{Source: "telegram", ChatID: "chat1"},
		At:        time.Now(),
	})
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	dec, err := sa.Decide(ctx, "telegram:1:200", superagent.OutcomeAccept, "valid bug", []string{"code_fix"})
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	tID := dec.TicketID

	// 2. Test /tickets
	cmdRes, err := eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/tickets",
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/tickets command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "Fix race condition in store") || !strings.Contains(cmdRes.Output, tID) {
		t.Fatalf("unexpected /tickets output: %s", cmdRes.Output)
	}

	// 3. Test /ticket <id>
	cmdRes, err = eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/ticket " + tID,
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/ticket command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "Fix race condition in store") || !strings.Contains(cmdRes.Output, "**Requester**: alice") {
		t.Fatalf("unexpected /ticket output: %s", cmdRes.Output)
	}

	// 4. Update ticket to blocked report -> test /attention
	ticket, _ := sa.GetTicket(ctx, tID)
	err = sa.Report(ctx, tID, ticket.AssignmentID, superagent.ReportBlocked, "Blocked on lock refactor", "Can we change the mutex interface?")
	if err != nil {
		t.Fatalf("Report failed: %v", err)
	}

	cmdRes, err = eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/attention",
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/attention command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "Can we change the mutex interface?") || !strings.Contains(cmdRes.Output, "person/answer") {
		t.Fatalf("unexpected /attention output: %s", cmdRes.Output)
	}

	// 5. Test /retry <id>
	cmdRes, err = eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/retry " + tID,
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/retry command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "restarted for retry") {
		t.Fatalf("unexpected /retry output: %s", cmdRes.Output)
	}

	// 6. Test /stop <id>
	cmdRes, err = eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/stop " + tID + " no longer needed",
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/stop command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "stopped. Mandate withdrawn") {
		t.Fatalf("unexpected /stop output: %s", cmdRes.Output)
	}

	// 7. Test /dismiss <decision_id>
	_, err = sa.Receive(ctx, superagent.Trigger{
		ID:        "telegram:1:201",
		Source:    "telegram",
		Scope:     "chat1",
		Thread:    "telegram:chat1_unavail",
		Actor:     superagent.Actor{ID: "alice"},
		Addressed: superagent.AddressDirect,
		Text:      "Test unavail trigger",
		Channel:   superagent.ChannelAddress{Source: "telegram", ChatID: "chat1"},
		At:        time.Now(),
	})
	if err != nil {
		t.Fatalf("Receive unavail failed: %v", err)
	}
	decUnavail, err := sa.Decide(ctx, "telegram:1:201", superagent.OutcomeUnavailable, "needs human guidance", nil)
	if err != nil {
		t.Fatalf("Decide unavailable failed: %v", err)
	}

	cmdRes, err = eng.HandleCommand(ctx, TurnRequest{
		SessionID: "s1",
		Channel:   "cli",
		Input:     "/dismiss " + decUnavail.ID,
	})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("/dismiss command failed: %v", err)
	}
	if !strings.Contains(cmdRes.Output, "dismissed") {
		t.Fatalf("unexpected /dismiss output: %s", cmdRes.Output)
	}
}
