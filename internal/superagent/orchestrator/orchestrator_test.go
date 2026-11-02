package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/react"
	"github.com/gosolu/react/provider/mock"
)

type dummyPolProvider struct {
	p superagent.Policy
}

func (d dummyPolProvider) CurrentPolicy() superagent.Policy { return d.p }

func TestOrchestratorRouteAndIntake(t *testing.T) {
	ctx := context.Background()
	store := superagent.NewMemStore()
	pol := superagent.Policy{
		Sources: map[string]superagent.SourcePolicy{
			"telegram": {Allowed: []string{"alice"}},
		},
	}
	eng := superagent.NewEngine(store, dummyPolProvider{p: pol})

	// Configure mock model to call orchestrator_route to accept the trigger
	mockP := mock.New(
		mock.ToolCalls("", mock.Call("orchestrator_route", RouteArgs{
			Actions: []RouteAction{
				{
					Kind:      "accept",
					TriggerID: "tr_tg_1",
					Reason:    "Valid bug report",
				},
			},
		})),
		mock.Text("Trigger routed and accepted."),
	)

	orch, err := New(Config{
		Engine:   eng,
		Provider: mockP,
		Model:    "mock",
		Store:    nil,
	})
	if err != nil {
		t.Fatalf("New orchestrator failed: %v", err)
	}

	// 1. Receive incoming trigger
	tr := superagent.Trigger{
		ID:        "tr_tg_1",
		Source:    "telegram",
		Thread:    "chat_1",
		Actor:     superagent.Actor{ID: "alice", Name: "Alice"},
		Addressed: superagent.AddressMention,
		Text:      "Please fix bug in auth",
		Channel:   superagent.ChannelAddress{Source: "telegram", ChatID: "chat_1"},
	}

	res, err := eng.Receive(ctx, tr)
	if err != nil || res != superagent.ReceiveEvaluating {
		t.Fatalf("receive failed: %v, res=%s", err, res)
	}

	// 2. Generate Candidate effect via Assessor
	eval, _ := store.GetActiveEvalForThread("chat_1")
	if eval == nil {
		t.Fatalf("no active eval found for thread")
	}

	err = store.Transact(ctx, func(tx superagent.Tx) error {
		tRec, _ := tx.GetTrigger(tr.ID)
		return orch.Candidate(tx, eval, []*superagent.TriggerRecord{tRec}, nil)
	})
	if err != nil {
		t.Fatalf("Candidate failed: %v", err)
	}

	// 3. Process relay effect via HandleRelay
	card := RenderCandidateCard(eval, []*superagent.TriggerRecord{{Trigger: tr}}, nil)
	err = orch.HandleRelay(ctx, superagent.Notice{
		Kind:   superagent.NoticeCandidate,
		EvalID: eval.ID,
		Text:   card,
		Wake:   true,
	})
	if err != nil {
		t.Fatalf("HandleRelay failed: %v", err)
	}

	// 4. Verify that the orchestrator_route accepted the trigger and created a Ticket!
	openTickets, _ := store.ListOpenTickets()
	if len(openTickets) != 1 {
		t.Fatalf("expected 1 open ticket created by orchestrator, got %d", len(openTickets))
	}
	if openTickets[0].OriginTrigger != tr.ID {
		t.Fatalf("expected origin trigger %s, got %s", tr.ID, openTickets[0].OriginTrigger)
	}
}

func TestOrchestratorPolicyTools(t *testing.T) {
	ctx := context.Background()
	store := superagent.NewMemStore()
	initialPol := superagent.Policy{
		AcceptMode: superagent.AcceptObserve,
		Rules: []superagent.Rule{
			{Name: "r1", Instructions: "rule one", Enabled: true},
		},
	}
	dynPol := superagent.NewDynamicPolicyProvider(initialPol)
	eng := superagent.NewEngine(store, dynPol)

	tools := NewOrchestratorTools(eng)
	toolMap := make(map[string]react.Tool)
	for _, tool := range tools {
		toolMap[tool.Spec().Name] = tool
	}

	// 1. Test orchestrator_policy_get
	getTool, ok := toolMap["orchestrator_policy_get"]
	if !ok {
		t.Fatalf("orchestrator_policy_get tool not found")
	}
	res, err := getTool.Call(ctx, json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("policy_get failed: %v, text=%s", err, res.Text())
	}
	out := res.Text()
	if !strings.Contains(out, "observe") || !strings.Contains(out, "r1") {
		t.Fatalf("unexpected policy_get output: %s", out)
	}

	// 2. Test orchestrator_policy_set_mode to auto
	setModeTool, ok := toolMap["orchestrator_policy_set_mode"]
	if !ok {
		t.Fatalf("orchestrator_policy_set_mode tool not found")
	}
	res, err = setModeTool.Call(ctx, json.RawMessage(`{"mode":"auto"}`))
	if err != nil || res.IsError {
		t.Fatalf("policy_set_mode failed: %v, text=%s", err, res.Text())
	}
	out = res.Text()
	if !strings.Contains(out, "accept mode is now auto") {
		t.Fatalf("unexpected set_mode output: %s", out)
	}
	if eng.Policy().Mode() != superagent.AcceptAuto {
		t.Fatalf("expected policy mode to be auto, got %s", eng.Policy().Mode())
	}

	// 3. Test orchestrator_policy_toggle_rule
	toggleTool, ok := toolMap["orchestrator_policy_toggle_rule"]
	if !ok {
		t.Fatalf("orchestrator_policy_toggle_rule tool not found")
	}
	res, err = toggleTool.Call(ctx, json.RawMessage(`{"rule_name":"r1","enabled":false}`))
	if err != nil || res.IsError {
		t.Fatalf("policy_toggle_rule failed: %v, text=%s", err, res.Text())
	}
	out = res.Text()
	if !strings.Contains(out, "rule r1 is now disabled") {
		t.Fatalf("unexpected toggle output: %s", out)
	}
	r1, found := eng.Policy().Rule("r1")
	if found {
		t.Fatalf("expected r1 to not be found in EnabledRules, but got %+v", r1)
	}
}
