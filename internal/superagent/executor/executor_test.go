package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/react/provider/mock"
)

type dummyPolProvider struct {
	p superagent.Policy
}

func (d dummyPolProvider) CurrentPolicy() superagent.Policy { return d.p }

func TestWorkerToolsAndRunner(t *testing.T) {
	ctx := context.Background()
	store := superagent.NewMemStore()
	pol := superagent.Policy{
		Sources: map[string]superagent.SourcePolicy{
			"feishu": {Allowed: []string{"user_1"}},
		},
	}
	eng := superagent.NewEngine(store, dummyPolProvider{p: pol})

	tmpWork, err := os.MkdirTemp("", "clapbot-work-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpWork) }()

	mockP := mock.New(
		mock.ToolCalls("", mock.Call("worker_report", ReportArgs{
			State:   "ready",
			Summary: "Fixed defect in parser",
		})),
		mock.Text("Work completed successfully."),
	)

	runner := NewWorkerRunner(Config{
		Engine:   eng,
		Repos:    store,
		Provider: mockP,
		Model:    "mock",
		Store:    nil,
		WorkRoot: tmpWork,
		MaxSlots: 2,
	})

	// 1. Create a ticket in store
	tr := superagent.Trigger{
		ID:        "tr_w1",
		Source:    "feishu",
		Thread:    "th_1",
		Actor:     superagent.Actor{ID: "user_1"},
		Addressed: superagent.AddressMention,
		Text:      "Fix parsing bug",
		Channel:   superagent.ChannelAddress{Source: "feishu", ChatID: "chat_1"},
	}
	_, _ = eng.Receive(ctx, tr)
	dec, err := eng.Decide(ctx, tr.ID, superagent.OutcomeAccept, "Fix parsing", nil)
	if err != nil || dec.TicketID == "" {
		t.Fatalf("decide failed: %v", err)
	}

	ticket, _ := store.GetTicket(dec.TicketID)
	as, _ := store.GetAssignment(ticket.AssignmentID)

	// 2. Launch worker runner
	err = runner.HandleLaunch(ctx, superagent.LaunchPayload{
		TicketID:     ticket.ID,
		AssignmentID: as.ID,
	})
	if err != nil {
		t.Fatalf("HandleLaunch failed: %v", err)
	}

	// 3. Verify ticket state after worker called worker_report ready
	ticketAfter, _ := store.GetTicket(ticket.ID)
	if ticketAfter.Status != superagent.TicketEnded || ticketAfter.EndReason != superagent.EndCompleted {
		t.Fatalf("expected ticket ended completed, got: %+v", ticketAfter)
	}
	if ticketAfter.LastReport == nil || ticketAfter.LastReport.Summary != "Fixed defect in parser" {
		t.Fatalf("expected last report from worker tool, got: %+v", ticketAfter.LastReport)
	}

	// 4. Verify ticket workspace directory was created
	dir := filepath.Join(tmpWork, ticket.Name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("ticket dir was not created at %s", dir)
	}
}
