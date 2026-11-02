package superagent

import (
	"context"
	"testing"
)

type mockPolicyProvider struct {
	p Policy
}

func (m mockPolicyProvider) CurrentPolicy() Policy { return m.p }

type mockAssessor struct {
	candidates []*Eval
}

func (m *mockAssessor) Candidate(tx Tx, eval *Eval, triggers []*TriggerRecord, tickets []*Ticket) error {
	m.candidates = append(m.candidates, eval)
	return nil
}

type mockExecutor struct {
	launches []string
	delivers []string
	ends     []string
}

func (m *mockExecutor) Launch(tx Tx, ticket *Ticket, assignment *Assignment, priority bool) error {
	m.launches = append(m.launches, ticket.ID)
	return nil
}

func (m *mockExecutor) Deliver(tx Tx, ticketID, assignmentID string, inputIDs []string, priority bool) error {
	m.delivers = append(m.delivers, ticketID)
	return nil
}

func (m *mockExecutor) End(tx Tx, ticketID, assignmentID, reason string, cancel bool) error {
	m.ends = append(m.ends, ticketID)
	return nil
}

type mockOutbox struct {
	replies []ReplyPayload
}

func (m *mockOutbox) Send(tx Tx, msg ReplyPayload) error {
	m.replies = append(m.replies, msg)
	return nil
}

type mockSources struct {
	listenMap map[string]bool
}

func (m mockSources) Listens(thread string) bool {
	if m.listenMap == nil {
		return true
	}
	return m.listenMap[thread]
}

func TestPolicyGate(t *testing.T) {
	pol := Policy{
		AcceptMode: AcceptObserve,
		Sources: map[string]SourcePolicy{
			"feishu": {
				Allowed: []string{"ou_allowed_123"},
				Bots:    []string{"bot_dependabot"},
				Default: Admission{
					ListedUnaddressed:   CellInferred,
					UnlistedAddressed:   CellIgnore,
					UnlistedUnaddressed: CellIgnore,
				},
			},
		},
	}

	// 1. Allowed person with direct mention -> explicit request
	tr1 := Trigger{
		Source:    "feishu",
		Actor:     Actor{ID: "ou_allowed_123"},
		Addressed: AddressMention,
	}
	str1, basis1 := pol.Gate(tr1, false)
	if str1 != StrengthExplicit || basis1 != BasisListed {
		t.Fatalf("expected explicit/listed, got %v/%v", str1, basis1)
	}

	// 2. Allowed person without mention -> inferred candidate
	tr2 := Trigger{
		Source:    "feishu",
		Actor:     Actor{ID: "ou_allowed_123"},
		Addressed: AddressNone,
	}
	str2, basis2 := pol.Gate(tr2, false)
	if str2 != StrengthInferred || basis2 != BasisTable {
		t.Fatalf("expected inferred/table, got %v/%v", str2, basis2)
	}

	// 3. Unlisted person with mention -> not admitted by default
	tr3 := Trigger{
		Source:    "feishu",
		Actor:     Actor{ID: "ou_stranger_456"},
		Addressed: AddressMention,
	}
	str3, _ := pol.Gate(tr3, false)
	if str3 != StrengthNone {
		t.Fatalf("expected none, got %v", str3)
	}

	// 4. Bot on bot list with mention -> inferred candidate
	tr4 := Trigger{
		Source:    "feishu",
		Actor:     Actor{ID: "bot_dependabot", Bot: true},
		Addressed: AddressMention,
	}
	str4, basis4 := pol.Gate(tr4, false)
	if str4 != StrengthInferred || basis4 != BasisBot {
		t.Fatalf("expected inferred/bot, got %v/%v", str4, basis4)
	}
}

func TestIntakeDeduplication(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	pol := Policy{
		Sources: map[string]SourcePolicy{
			"telegram": {Allowed: []string{"user1"}},
		},
	}
	eng := NewEngine(store, mockPolicyProvider{p: pol})

	tr := Trigger{
		ID:        "msg_1",
		Source:    "telegram",
		Thread:    "chat_100",
		Actor:     Actor{ID: "user1"},
		Addressed: AddressMention,
		Text:      "build a calculator",
	}

	res1, err := eng.Receive(ctx, tr)
	if err != nil || res1 != ReceiveEvaluating {
		t.Fatalf("first receive: %v, err=%v", res1, err)
	}

	res2, err := eng.Receive(ctx, tr)
	if err != nil || res2 != ReceiveDuplicate {
		t.Fatalf("second receive should be duplicate: %v, err=%v", res2, err)
	}
}

func TestExplicitAcceptAndTicketLifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	assessor := &mockAssessor{}
	executor := &mockExecutor{}
	outbox := &mockOutbox{}

	pol := Policy{
		AcceptMode: AcceptObserve,
		Sources: map[string]SourcePolicy{
			"telegram": {Allowed: []string{"alice"}},
		},
	}

	eng := NewEngine(store, mockPolicyProvider{p: pol},
		WithAssessor(assessor),
		WithExecutor(executor),
		WithOutbox(outbox),
	)

	tr := Trigger{
		ID:        "tr_101",
		Source:    "telegram",
		Thread:    "tg:chat_1",
		Actor:     Actor{ID: "alice", Name: "Alice"},
		Addressed: AddressMention,
		Text:      "Please fix bug in login form",
		Channel:   ChannelAddress{Source: "telegram", ChatID: "chat_1", ReplyTo: "101"},
	}

	res, err := eng.Receive(ctx, tr)
	if err != nil || res != ReceiveEvaluating {
		t.Fatalf("receive: %v, err: %v", res, err)
	}
	if len(assessor.candidates) != 1 {
		t.Fatalf("expected candidate delivered, got %d", len(assessor.candidates))
	}

	// Main session decides to ACCEPT.
	dec, err := eng.Decide(ctx, tr.ID, OutcomeAccept, "Clear bug report", nil)
	if err != nil {
		t.Fatalf("decide accept: %v", err)
	}
	if dec.TicketID == "" {
		t.Fatalf("expected ticket created")
	}

	// Check ticket in store.
	ticket, err := store.GetTicket(dec.TicketID)
	if err != nil || ticket == nil {
		t.Fatalf("ticket not in store: %v", err)
	}
	if ticket.Status != TicketOpen {
		t.Fatalf("ticket not open: %s", ticket.Status)
	}
	if !ticket.Acknowledged {
		t.Fatalf("ticket not marked acknowledged")
	}
	if len(outbox.replies) == 0 || outbox.replies[0].Purpose != PurposeAcknowledgement {
		t.Fatalf("expected acknowledgement in outbox")
	}
	if len(executor.launches) != 1 || executor.launches[0] != ticket.ID {
		t.Fatalf("expected executor launch for ticket")
	}

	// Worker reports progress.
	err = eng.Report(ctx, ticket.ID, ticket.AssignmentID, ReportProgress, "Working on reproduction", "")
	if err != nil {
		t.Fatalf("report progress: %v", err)
	}

	// Worker reports question (Attention).
	err = eng.Report(ctx, ticket.ID, ticket.AssignmentID, ReportWaiting, "Need login credentials", "Can you provide the test user credentials?")
	if err != nil {
		t.Fatalf("report question: %v", err)
	}

	ticket, _ = store.GetTicket(ticket.ID)
	if ticket.Attention == "" {
		t.Fatalf("expected attention set on ticket")
	}
	as, _ := store.GetAssignment(ticket.AssignmentID)
	stage := ComputeStage(ticket, as)
	if stage != StagePersonAnswer {
		t.Fatalf("expected stage person/answer, got %s", stage)
	}

	// User follows up on thread!
	followUp := Trigger{
		ID:        "tr_102",
		Source:    "telegram",
		Thread:    "tg:chat_1",
		Actor:     Actor{ID: "alice", Name: "Alice"},
		Addressed: AddressNone,
		Text:      "Use test user user@example.com / password123",
		Channel:   ChannelAddress{Source: "telegram", ChatID: "chat_1", ReplyTo: "102"},
	}
	fRes, err := eng.Receive(ctx, followUp)
	if err != nil || fRes != ReceiveOwned {
		t.Fatalf("expected follow-up owned by ticket: %v, err=%v", fRes, err)
	}

	// Worker reports ready.
	err = eng.Report(ctx, ticket.ID, ticket.AssignmentID, ReportReady, "Fixed login bug and added regression test", "")
	if err != nil {
		t.Fatalf("report ready: %v", err)
	}

	ticket, _ = store.GetTicket(ticket.ID)
	if ticket.Status != TicketEnded || ticket.EndReason != EndCompleted {
		t.Fatalf("expected ticket completed after ready without PR, got status=%s, reason=%s", ticket.Status, ticket.EndReason)
	}
}

func TestInferredObserveVsAuto(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	executor := &mockExecutor{}

	// 1. Observe mode: positive assessment records WouldAccept = true and does NOT create a Ticket.
	polObserve := Policy{
		AcceptMode: AcceptObserve,
		Rules: []Rule{
			{Name: "deps", Instructions: "Update dependencies", Enabled: true},
		},
		Sources: map[string]SourcePolicy{
			"github": {
				Bots: []string{"dependabot[bot]"},
				Default: Admission{
					UnlistedAddressed: CellInferred,
				},
			},
		},
	}
	engObserve := NewEngine(store, mockPolicyProvider{p: polObserve}, WithExecutor(executor))

	tr1 := Trigger{
		ID:        "gh_1",
		Source:    "github",
		Thread:    "gh:repo/pull/10",
		Actor:     Actor{ID: "dependabot[bot]", Bot: true},
		Addressed: AddressMention,
		Text:      "Bump golang.org/x/net from 0.20 to 0.21",
	}

	res, _ := engObserve.Receive(ctx, tr1)
	if res != ReceiveEvaluating {
		t.Fatalf("expected evaluating, got %v", res)
	}

	dec, err := engObserve.Decide(ctx, tr1.ID, OutcomeAccept, "Security bump", []string{"deps"})
	if err != nil {
		t.Fatalf("decide observe: %v", err)
	}
	if !dec.WouldAccept || dec.Outcome != OutcomeIgnore {
		t.Fatalf("expected WouldAccept=true and OutcomeIgnore in observe mode, got outcome=%s", dec.Outcome)
	}
	if len(executor.launches) != 0 {
		t.Fatalf("should not launch worker in observe mode")
	}

	// 2. Auto mode: positive assessment accepts and launches a Ticket.
	polAuto := Policy{
		AcceptMode: AcceptAuto,
		Rules: []Rule{
			{Name: "deps", Instructions: "Update dependencies", Enabled: true},
		},
		Sources: map[string]SourcePolicy{
			"github": {
				Bots: []string{"dependabot[bot]"},
				Default: Admission{
					UnlistedAddressed: CellInferred,
				},
			},
		},
	}
	engAuto := NewEngine(store, mockPolicyProvider{p: polAuto}, WithExecutor(executor))

	tr2 := Trigger{
		ID:        "gh_2",
		Source:    "github",
		Thread:    "gh:repo/pull/11",
		Actor:     Actor{ID: "dependabot[bot]", Bot: true},
		Addressed: AddressMention,
		Text:      "Bump lodash from 4.17.20 to 4.17.21",
	}

	_, _ = engAuto.Receive(ctx, tr2)
	dec2, err := engAuto.Decide(ctx, tr2.ID, OutcomeAccept, "Dependabot update", []string{"deps"})
	if err != nil {
		t.Fatalf("decide auto: %v", err)
	}
	if dec2.Outcome != OutcomeAccept || dec2.TicketID == "" {
		t.Fatalf("expected accepted ticket in auto mode")
	}
	if len(executor.launches) != 1 {
		t.Fatalf("expected worker launched in auto mode")
	}
}

func TestStopAndResumeRevalidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	executor := &mockExecutor{}

	pol := Policy{
		AcceptMode: AcceptAuto,
		Sources: map[string]SourcePolicy{
			"telegram": {Allowed: []string{"bob"}},
		},
	}

	eng := NewEngine(store, mockPolicyProvider{p: pol}, WithExecutor(executor))

	tr := Trigger{
		ID:        "tr_200",
		Source:    "telegram",
		Thread:    "chat_200",
		Actor:     Actor{ID: "bob"},
		Addressed: AddressMention,
		Text:      "Refactor store",
	}
	_, _ = eng.Receive(ctx, tr)
	dec, _ := eng.Decide(ctx, tr.ID, OutcomeAccept, "Refactor task", nil)
	ticketID := dec.TicketID

	// Human stops the ticket.
	err := eng.StopTicket(ctx, ticketID, "Change of plans")
	if err != nil {
		t.Fatalf("stop ticket: %v", err)
	}

	ticket, _ := store.GetTicket(ticketID)
	if !ticket.Stopped() || ticket.Status != TicketEnded {
		t.Fatalf("expected ticket stopped and ended")
	}

	// Later message to a stopped ticket does NOT resume it.
	laterTr := Trigger{
		ID:        "tr_201",
		Source:    "telegram",
		Thread:    "chat_200",
		Actor:     Actor{ID: "bob"},
		Addressed: AddressNone,
		Text:      "Can we resume?",
	}
	res, err := eng.Receive(ctx, laterTr)
	if err != nil {
		t.Fatalf("receive later: %v", err)
	}
	// Because the stopped ticket takes nothing, this message takes the Eval path!
	if res != ReceiveEvaluating {
		t.Fatalf("expected message on stopped thread to go to Eval, got %v", res)
	}
}
