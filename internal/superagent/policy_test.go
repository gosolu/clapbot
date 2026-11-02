package superagent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPolicyEffectComputationAndFormatting(t *testing.T) {
	oldPol := Policy{
		AcceptMode: AcceptAuto,
		Rules: []Rule{
			{Name: "bug_fix", Instructions: "fix all reported bugs", Enabled: true},
			{Name: "code_cleanup", Instructions: "remove dead code", Enabled: true},
			{Name: "docs_update", Instructions: "update markdown docs", Enabled: true},
		},
		Sources: map[string]SourcePolicy{
			"feishu": {
				Allowed: []string{"alice", "bob"},
			},
		},
	}

	newPol := Policy{
		AcceptMode: AcceptObserve, // left auto
		Rules: []Rule{
			{Name: "bug_fix", Instructions: "fix critical security bugs only", Enabled: true}, // changed
			{Name: "code_cleanup", Instructions: "remove dead code", Enabled: false},         // disabled
			{Name: "docs_update", Instructions: "update markdown docs", Enabled: true},       // unchanged
		},
		Sources: map[string]SourcePolicy{
			"feishu": {
				Allowed: []string{"alice"}, // bob removed
			},
		},
	}

	t1 := &Ticket{
		ID:     "t-1",
		Status: TicketOpen,
		Mandate: Mandate{
			Strength:     StrengthInferred,
			MatchedRules: []string{"bug_fix"},
			Requester:    Actor{ID: "alice"},
			Source:       "feishu",
		},
	}

	t2 := &Ticket{
		ID:     "t-2",
		Status: TicketOpen,
		Mandate: Mandate{
			Strength:     StrengthExplicit,
			MatchedRules: []string{"code_cleanup"},
			Requester:    Actor{ID: "alice"},
			Source:       "feishu",
		},
	}

	t3 := &Ticket{
		ID:     "t-3",
		Status: TicketOpen,
		Mandate: Mandate{
			Strength:     StrengthExplicit,
			MatchedRules: []string{"docs_update"},
			Requester:    Actor{ID: "bob"},
			Source:       "feishu",
		},
	}

	t4Ended := &Ticket{
		ID:     "t-4",
		Status: TicketEnded,
		Mandate: Mandate{
			Strength:     StrengthExplicit,
			MatchedRules: []string{"code_cleanup"},
			Requester:    Actor{ID: "alice"},
			Source:       "feishu",
		},
	}

	tickets := []*Ticket{t1, t2, t3, t4Ended}
	effects := ComputePolicyEffects(tickets, oldPol, newPol)

	// t-4 is ended, should have no effect
	if _, ok := effects["t-4"]; ok {
		t.Fatalf("expected ended ticket t-4 to be ignored in effects")
	}

	// Verify t-1 (rule changed + proactive left auto)
	eff1, ok := effects["t-1"]
	if !ok {
		t.Fatalf("expected effect for t-1")
	}
	if len(eff1.Rules) != 1 || eff1.Rules[0].Change != "changed" {
		t.Fatalf("expected changed rule for t-1, got %+v", eff1.Rules)
	}
	if eff1.Proactive == nil || *eff1.Proactive {
		t.Fatalf("expected proactive=false for t-1")
	}
	msg1 := FormatPolicyMessage(eff1)
	if !strings.Contains(msg1, "changed. Keep the remaining work within it as it reads now: fix critical security bugs only") {
		t.Fatalf("unexpected message 1: %s", msg1)
	}
	if !strings.Contains(msg1, "Work inferred from a rule is no longer accepted") {
		t.Fatalf("unexpected proactive notice: %s", msg1)
	}

	// Verify t-2 (rule disabled)
	eff2, ok := effects["t-2"]
	if !ok {
		t.Fatalf("expected effect for t-2")
	}
	if len(eff2.Rules) != 1 || eff2.Rules[0].Change != "disabled" {
		t.Fatalf("expected disabled rule for t-2, got %+v", eff2.Rules)
	}
	msg2 := FormatPolicyMessage(eff2)
	if !strings.Contains(msg2, "Rule code_cleanup, which this ticket was accepted under, no longer applies") {
		t.Fatalf("unexpected message 2: %s", msg2)
	}

	// Verify t-3 (requester bob removed from allowed)
	eff3, ok := effects["t-3"]
	if !ok {
		t.Fatalf("expected effect for t-3")
	}
	if eff3.RequesterAllowed == nil || *eff3.RequesterAllowed {
		t.Fatalf("expected requesterAllowed=false for t-3")
	}
	msg3 := FormatPolicyMessage(eff3)
	if !strings.Contains(msg3, "The sender this ticket is for, bob, may no longer start work here") {
		t.Fatalf("unexpected message 3: %s", msg3)
	}
}

func TestPolicyChangedActiveEvalsInvalidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	assessor := &mockAssessor{}

	initialPol := Policy{
		AcceptMode: AcceptObserve,
		Rules: []Rule{
			{Name: "r1", Instructions: "inst1", Enabled: true},
		},
	}
	dynPolicy := NewDynamicPolicyProvider(initialPol)
	eng := NewEngine(store, dynPolicy, WithAssessor(assessor))

	// Create thread and trigger
	now := time.Now()
	tr1 := &TriggerRecord{
		Trigger: Trigger{
			ID:     "tr-1",
			Source: "telegram",
			Scope:  "chat1",
			Actor:  Actor{ID: "user1"},
			Text:   "hello",
			Thread: "th-1",
			At:     now,
		},
		Disposition: DispositionEvaluating,
		ReceivedAt:  now,
	}
	tr2 := &TriggerRecord{
		Trigger: Trigger{
			ID:     "tr-2",
			Source: "telegram",
			Scope:  "chat1",
			Actor:  Actor{ID: "user2"},
			Text:   "world",
			Thread: "th-1",
			At:     now,
		},
		Disposition: DispositionEvaluating,
		ReceivedAt:  now,
	}

	ev := &Eval{
		ID:            "eval-1",
		Thread:        "th-1",
		TriggerIDs:    []string{"tr-1", "tr-2"},
		Decided:       map[string]string{"tr-1": "dec-1"}, // tr-1 decided, tr-2 undecided
		PolicyVersion: initialPol.Version(),
		Status:        EvalDelivered,
		CreatedAt:     now,
	}

	th := &Thread{
		Key:    "th-1",
		EvalID: ev.ID,
		LastAt: now,
	}

	_ = store.SaveTrigger(tr1)
	_ = store.SaveTrigger(tr2)
	_ = store.SaveEval(ev)
	_ = store.SaveThread(th)

	// Update policy to a new version
	newPol := initialPol.WithRule(Rule{Name: "r1", Instructions: "inst2 (updated)", Enabled: true})
	if newPol.Version() == initialPol.Version() {
		t.Fatalf("expected version hash to change")
	}

	err := eng.UpdatePolicy(ctx, newPol)
	if err != nil {
		t.Fatalf("UpdatePolicy failed: %v", err)
	}

	// Verify old eval is finished
	oldEv, _ := store.GetEval("eval-1")
	if oldEv.Status != EvalDone {
		t.Fatalf("expected old eval status to be done, got %s", oldEv.Status)
	}

	// Verify tr-2 received unavailable decision with policyChanged reason
	decID := oldEv.Decided["tr-2"]
	if decID == "" {
		t.Fatalf("expected decision recorded for undecided tr-2")
	}
	dec, _ := store.GetDecision(decID)
	if dec.Outcome != OutcomeUnavailable || dec.Reason != "policyChanged" {
		t.Fatalf("expected unavailable/policyChanged decision, got %+v", dec)
	}

	// Verify thread has started a new eval with the new policy version
	thUpdated, _ := store.GetThread("th-1")
	if thUpdated.EvalID == "" || thUpdated.EvalID == "eval-1" {
		t.Fatalf("expected new eval ID on thread, got %s", thUpdated.EvalID)
	}

	newEval, _ := store.GetEval(thUpdated.EvalID)
	if newEval == nil {
		t.Fatalf("expected new eval in store")
	}
	if newEval.PolicyVersion != newPol.Version() {
		t.Fatalf("expected new eval version %s, got %s", newPol.Version(), newEval.PolicyVersion)
	}
	if len(newEval.TriggerIDs) != 1 || newEval.TriggerIDs[0] != "tr-2" {
		t.Fatalf("expected tr-2 in new eval triggers, got %+v", newEval.TriggerIDs)
	}

	// Verify assessor received the new candidate
	if len(assessor.candidates) == 0 {
		t.Fatalf("expected assessor candidate delivery")
	}
}

func TestPolicyChangedOpenTicketsNotification(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	executor := &mockExecutor{}

	initialPol := Policy{
		AcceptMode: AcceptAuto,
		Rules: []Rule{
			{Name: "code_cleanup", Instructions: "clean code", Enabled: true},
			{Name: "docs", Instructions: "update docs", Enabled: true},
		},
	}
	dynPolicy := NewDynamicPolicyProvider(initialPol)
	eng := NewEngine(store, dynPolicy, WithExecutor(executor))

	now := time.Now()
	// Open ticket under code_cleanup with running assignment
	as1 := &Assignment{
		ID:        "as-1",
		TicketID:  "t-1",
		Status:    AssignmentRunning,
		CreatedAt: now,
	}
	t1 := &Ticket{
		ID:           "t-1",
		Name:         "task-cleanup",
		Status:       TicketOpen,
		AssignmentID: as1.ID,
		Mandate: Mandate{
			Strength:     StrengthExplicit,
			MatchedRules: []string{"code_cleanup"},
			Requester:    Actor{ID: "alice"},
			Source:       "telegram",
		},
		CreatedAt: now,
	}

	// Open ticket under docs (unaffected)
	t2 := &Ticket{
		ID:     "t-2",
		Name:   "task-docs",
		Status: TicketOpen,
		Mandate: Mandate{
			Strength:     StrengthExplicit,
			MatchedRules: []string{"docs"},
			Requester:    Actor{ID: "bob"},
			Source:       "telegram",
		},
		CreatedAt: now.Add(time.Minute),
	}

	_ = store.SaveAssignment(as1)
	_ = store.SaveTicket(t1)
	_ = store.SaveTicket(t2)

	// Disable code_cleanup in new policy
	newPol := initialPol.WithRuleEnabled("code_cleanup", false)
	err := eng.UpdatePolicy(ctx, newPol)
	if err != nil {
		t.Fatalf("UpdatePolicy failed: %v", err)
	}

	// Verify t-1 received an InputPolicy
	inputsT1, _ := store.GetInputsForTicket("t-1")
	if len(inputsT1) != 1 {
		t.Fatalf("expected 1 input for t-1, got %d", len(inputsT1))
	}
	if inputsT1[0].Kind != InputPolicy {
		t.Fatalf("expected InputPolicy, got %s", inputsT1[0].Kind)
	}
	if !strings.Contains(inputsT1[0].Text, "Rule code_cleanup, which this ticket was accepted under, no longer applies") {
		t.Fatalf("unexpected input text: %s", inputsT1[0].Text)
	}
	// Verify executor deliver was invoked
	if len(executor.delivers) != 1 || executor.delivers[0] != "t-1" {
		t.Fatalf("expected deliver call for t-1, got %+v", executor.delivers)
	}

	// Verify t-2 received NO inputs (unaffected)
	inputsT2, _ := store.GetInputsForTicket("t-2")
	if len(inputsT2) != 0 {
		t.Fatalf("expected 0 inputs for unaffected t-2, got %d", len(inputsT2))
	}
}
