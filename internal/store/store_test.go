package store

import (
	"context"
	"testing"

	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/react"
)

func TestSQLiteStoreCRUDAndFTS(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer func() { _ = st.Close() }()

	sess, err := st.EnsureSession(ctx, "s1", "/tmp/work", "default")
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if sess.WorkspaceDir != "/tmp/work" || sess.Profile != "default" {
		t.Fatalf("unexpected session metadata: %+v", sess)
	}

	if err := st.Append(ctx, "s1", react.UserText("Hello Clapbot!"), react.AssistantText("Hi there!")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	msgs, err := st.Load(ctx, "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Text() != "Hello Clapbot!" || msgs[1].Text() != "Hi there!" {
		t.Fatalf("unexpected loaded messages: %+v", msgs)
	}

	sessAfter, err := st.GetSession(ctx, "s1")
	if err != nil || sessAfter == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sessAfter.Title != "Hello Clapbot!" || sessAfter.MessageCount != 2 {
		t.Fatalf("expected auto title and count=2, got %+v", sessAfter)
	}

	if err := st.SetBoundSession(ctx, "telegram", "12345", "s1"); err != nil {
		t.Fatalf("SetBoundSession: %v", err)
	}
	bound, err := st.GetBoundSession(ctx, "telegram", "12345")
	if err != nil || bound != "s1" {
		t.Fatalf("expected bound session s1, got %q (err=%v)", bound, err)
	}

	if err := st.IndexMemoryEntry(ctx, "architecture", "We chose modernc.org/sqlite for pure Go.", "MEMORY.md"); err != nil {
		t.Fatalf("IndexMemoryEntry: %v", err)
	}
	hits, err := st.SearchMemory(ctx, "modernc", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least 1 search hit for modernc")
	}
}

type dummyPolicyProvider struct{ p superagent.Policy }

func (d dummyPolicyProvider) CurrentPolicy() superagent.Policy { return d.p }

func TestSQLiteStoreSuperAgentIntegration(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer func() { _ = st.Close() }()

	pol := superagent.Policy{
		Sources: map[string]superagent.SourcePolicy{
			"feishu": {Allowed: []string{"ou_user_1"}},
		},
	}
	eng := superagent.NewEngine(st, dummyPolicyProvider{p: pol})

	tr := superagent.Trigger{
		ID:        "msg_feishu_1",
		Source:    "feishu",
		Thread:    "chat_feishu_100",
		Actor:     superagent.Actor{ID: "ou_user_1", Name: "FeishuUser"},
		Addressed: superagent.AddressMention,
		Text:      "Deploy new release",
		Channel:   superagent.ChannelAddress{Source: "feishu", ChatID: "chat_feishu_100"},
	}

	res, err := eng.Receive(ctx, tr)
	if err != nil || res != superagent.ReceiveEvaluating {
		t.Fatalf("receive error: %v, res: %s", err, res)
	}

	dec, err := eng.Decide(ctx, tr.ID, superagent.OutcomeAccept, "Approved deploy", nil)
	if err != nil {
		t.Fatalf("decide error: %v", err)
	}
	if dec.TicketID == "" {
		t.Fatalf("ticket id empty")
	}

	var ticket *superagent.Ticket
	_ = st.Transact(ctx, func(tx superagent.Tx) error {
		var err error
		ticket, err = tx.GetTicket(dec.TicketID)
		return err
	})
	if ticket == nil || ticket.Status != superagent.TicketOpen {
		t.Fatalf("expected open ticket in sqlite, got: %+v", ticket)
	}
}
