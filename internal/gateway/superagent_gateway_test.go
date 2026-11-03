package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/react/provider/mock"
)

func TestSuperAgentGatewayEndpoints(t *testing.T) {
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

	eng, err := runtime.NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	srv := NewServer("127.0.0.1:0", eng)
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	baseURL := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Test GET /api/overview before any tickets
	resp, err := client.Get(baseURL + "/api/overview")
	if err != nil {
		t.Fatalf("GET /api/overview failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/overview, got %d", resp.StatusCode)
	}
	var ov superagent.OverviewView
	if err := json.NewDecoder(resp.Body).Decode(&ov); err != nil {
		t.Fatalf("decode overview failed: %v", err)
	}
	if ov.AcceptMode != superagent.AcceptAuto {
		t.Fatalf("expected accept_mode auto, got %s", ov.AcceptMode)
	}
	if ov.TotalTickets != 0 {
		t.Fatalf("expected 0 total tickets, got %d", ov.TotalTickets)
	}

	// 2. Ingest a trigger that gets auto-accepted into a ticket
	sa := eng.SuperAgent()
	recRes, err := sa.Receive(ctx, superagent.Trigger{
		ID:        "telegram:1:100",
		Source:    "telegram",
		Scope:     "chat1",
		Thread:    "telegram:chat1",
		Actor:     superagent.Actor{ID: "alice"},
		Addressed: superagent.AddressDirect,
		Text:      "Fix null pointer exception in auth module",
		Channel:   superagent.ChannelAddress{Source: "telegram", ChatID: "chat1"},
		At:        time.Now(),
	})
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	if recRes != superagent.ReceiveEvaluating {
		t.Fatalf("expected ReceiveEvaluating, got %s", recRes)
	}

	// Route & decide accept via engine
	dec, err := sa.Decide(ctx, "telegram:1:100", superagent.OutcomeAccept, "valid bug report", []string{"code_fix"})
	if err != nil {
		t.Fatalf("Decide accept failed: %v", err)
	}
	ticketID := dec.TicketID
	if ticketID == "" {
		t.Fatalf("expected ticketID from accept decision")
	}

	// 3. Test GET /api/tickets
	resp, err = client.Get(baseURL + "/api/tickets")
	if err != nil {
		t.Fatalf("GET /api/tickets failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var ticketsResp struct {
		Tickets []superagent.TicketSummaryView `json:"tickets"`
		Total   int                            `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ticketsResp); err != nil {
		t.Fatalf("decode tickets failed: %v", err)
	}
	if ticketsResp.Total != 1 || ticketsResp.Tickets[0].ID != ticketID {
		t.Fatalf("expected ticket %s, got %+v", ticketID, ticketsResp)
	}

	// 4. Test GET /api/tickets/{id}
	resp, err = client.Get(baseURL + "/api/tickets/" + ticketID)
	if err != nil {
		t.Fatalf("GET /api/tickets/{id} failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var detail superagent.TicketDetailView
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode ticket detail failed: %v", err)
	}
	if detail.Ticket.ID != ticketID || detail.Ticket.Title != "Fix null pointer exception in auth module" {
		t.Fatalf("unexpected ticket detail: %+v", detail)
	}

	// 5. Worker reports blocked with question -> Stage becomes person/blocked
	ticket, _ := sa.GetTicket(ctx, ticketID)
	asID := ticket.AssignmentID
	err = sa.Report(ctx, ticketID, asID, superagent.ReportBlocked, "Blocked on database migration credentials", "Do we have staging credentials?")
	if err != nil {
		t.Fatalf("Report failed: %v", err)
	}

	// 6. Test GET /api/attention -> Ticket must appear!
	resp, err = client.Get(baseURL + "/api/attention")
	if err != nil {
		t.Fatalf("GET /api/attention failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var att superagent.AttentionItem
	if err := json.NewDecoder(resp.Body).Decode(&att); err != nil {
		t.Fatalf("decode attention failed: %v", err)
	}
	if len(att.Tickets) != 1 || att.Tickets[0].TicketID != ticketID {
		t.Fatalf("expected ticket %s in attention, got %+v", ticketID, att.Tickets)
	}
	if att.Tickets[0].Stage != superagent.StagePersonAnswer || !att.Tickets[0].Stage.HumanAttention() {
		t.Fatalf("expected stage person/answer with human attention, got %s", att.Tickets[0].Stage)
	}

	// 7. Test POST /api/tickets/{id}/retry
	resp, err = client.Post(baseURL+"/api/tickets/"+ticketID+"/retry", "application/json", nil)
	if err != nil {
		t.Fatalf("POST retry failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from retry, got %d", resp.StatusCode)
	}
	tAfterRetry, _ := sa.GetTicket(ctx, ticketID)
	if !tAfterRetry.ReleasedByHuman {
		t.Fatalf("expected ReleasedByHuman true after retry")
	}

	// 8. Test POST /api/tickets/{id}/stop
	stopPayload := bytes.NewBufferString(`{"reason":"not needed anymore"}`)
	resp, err = client.Post(baseURL+"/api/tickets/"+ticketID+"/stop", "application/json", stopPayload)
	if err != nil {
		t.Fatalf("POST stop failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from stop, got %d", resp.StatusCode)
	}
	tAfterStop, _ := sa.GetTicket(ctx, ticketID)
	if tAfterStop.Status != superagent.TicketEnded || !tAfterStop.Stopped() {
		t.Fatalf("expected ticket ended and stopped")
	}

	// 9. Ingest trigger and record unavailable decision
	_, err = sa.Receive(ctx, superagent.Trigger{
		ID:        "telegram:1:101",
		Source:    "telegram",
		Scope:     "chat1",
		Thread:    "telegram:chat1_unavail",
		Actor:     superagent.Actor{ID: "alice"},
		Addressed: superagent.AddressDirect,
		Text:      "Evaluate this ambiguous task",
		Channel:   superagent.ChannelAddress{Source: "telegram", ChatID: "chat1"},
		At:        time.Now(),
	})
	if err != nil {
		t.Fatalf("Receive 2 failed: %v", err)
	}
	decUnavail, err := sa.Decide(ctx, "telegram:1:101", superagent.OutcomeUnavailable, "ambiguous instructions", nil)
	if err != nil {
		t.Fatalf("Decide unavailable failed: %v", err)
	}

	// 10. Test GET /api/decisions
	resp, err = client.Get(baseURL + "/api/decisions?outcome=unavailable")
	if err != nil {
		t.Fatalf("GET /api/decisions failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/decisions, got %d", resp.StatusCode)
	}
	var decResp struct {
		Decisions []*superagent.Decision `json:"decisions"`
		Total     int                    `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decResp); err != nil {
		t.Fatalf("decode decisions failed: %v", err)
	}
	if decResp.Total == 0 {
		t.Fatalf("expected at least 1 unavailable decision")
	}

	// 11. Test POST /api/decisions/{id}/dismiss
	resp, err = client.Post(baseURL+"/api/decisions/"+decUnavail.ID+"/dismiss", "application/json", nil)
	if err != nil {
		t.Fatalf("POST dismiss failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK from dismiss, got %d: %s", resp.StatusCode, string(body))
	}
	// Verify dismissed in attention
	attAfterDismiss, _ := sa.GetAttention(ctx)
	for _, d := range attAfterDismiss.UnavailableEvals {
		if d.ID == decUnavail.ID {
			t.Fatalf("expected decision %s to be dismissed from attention", decUnavail.ID)
		}
	}
}
