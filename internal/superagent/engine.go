package superagent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Engine is the central super-agent domain coordinator. All state transitions
// run through transactions on Repositories.
type Engine struct {
	repos    Repositories
	policy   PolicyProvider
	sources  SourceScope
	assessor Assessor
	executor Executor
	outbox   Outbox
	now      func() time.Time
}

// Option customizes Engine construction.
type Option func(*Engine)

// WithSourceScope configures the source listener checker.
func WithSourceScope(s SourceScope) Option {
	return func(e *Engine) { e.sources = s }
}

// WithAssessor configures candidate delivery to the orchestrator.
func WithAssessor(a Assessor) Option {
	return func(e *Engine) { e.assessor = a }
}

// WithExecutor configures worker session management.
func WithExecutor(x Executor) Option {
	return func(e *Engine) { e.executor = x }
}

// WithOutbox configures message sending.
func WithOutbox(o Outbox) Option {
	return func(e *Engine) { e.outbox = o }
}

// WithNow overrides the clock (useful for unit tests).
func WithNow(fn func() time.Time) Option {
	return func(e *Engine) { e.now = fn }
}

// NewEngine creates an Engine instance.
func NewEngine(repos Repositories, policy PolicyProvider, opts ...Option) *Engine {
	e := &Engine{
		repos:  repos,
		policy: policy,
		now:    time.Now,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// SetAssessor updates the Assessor port after construction.
func (e *Engine) SetAssessor(a Assessor) {
	e.assessor = a
}

// SetExecutor updates the Executor port after construction.
func (e *Engine) SetExecutor(x Executor) {
	e.executor = x
}

// EvalCandidate is the view of an Eval and its triggers delivered to the main session.
type EvalCandidate struct {
	EvalID   string           `json:"eval_id"`
	Thread   string           `json:"thread"`
	Triggers []*TriggerRecord `json:"triggers"`
	Tickets  []*Ticket        `json:"tickets"`
	Deadline time.Time        `json:"deadline"`
}

// =============================================================================
// EngineForSources
// =============================================================================

// Receive ingests a Source Trigger, deduplicating, fanning out, or queueing for Eval.
func (e *Engine) Receive(ctx context.Context, t Trigger) (res ReceiveResult, err error) {
	err = e.repos.Transact(ctx, func(tx Tx) error {
		var err error
		res, err = e.processIntake(tx, t)
		return err
	})
	return
}

// BindThread makes an open Ticket follow a Thread (e.g. an opened PR).
func (e *Engine) BindThread(ctx context.Context, ticketID, threadKey string, own bool) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		t, err := tx.GetTicket(ticketID)
		if err != nil || t == nil {
			return fmt.Errorf("ticket %s not found", ticketID)
		}
		kind := SubAttached
		if own {
			kind = SubOrigin
		}
		return tx.SaveSubscription(&Subscription{
			TicketID:  ticketID,
			Thread:    threadKey,
			Kind:      kind,
			CreatedAt: e.now(),
		})
	})
}

// ThreadClosed handles a Thread closing (e.g. a PR merged or closed).
func (e *Engine) ThreadClosed(ctx context.Context, threadKey, outcome string, at time.Time) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.threadClosedTx(tx, threadKey, outcome, at)
	})
}

// =============================================================================
// EngineForMainSession
// =============================================================================

// CandidateDelivered marks an Eval's candidate delivered so its deadline starts counting.
func (e *Engine) CandidateDelivered(ctx context.Context, evalID string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		ev, err := tx.GetEval(evalID)
		if err != nil || ev == nil {
			return fmt.Errorf("eval %s not found", evalID)
		}
		now := e.now()
		deadline := now.Add(90 * time.Second)
		ev.Status = EvalDelivered
		ev.DeliveredAt = &now
		ev.Deadline = &deadline
		return tx.SaveEval(ev)
	})
}

// Decide records a decision (accept, stop, ignore, unavailable) for a Trigger.
func (e *Engine) Decide(ctx context.Context, triggerID string, outcome Outcome, reason string, matchedRules []string) (*Decision, error) {
	var dec *Decision
	err := e.repos.Transact(ctx, func(tx Tx) error {
		var err error
		dec, err = e.decideTx(tx, triggerID, outcome, reason, matchedRules)
		return err
	})
	return dec, err
}

// Attach routes a Trigger to an existing Ticket.
func (e *Engine) Attach(ctx context.Context, ticketID, triggerID, note string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.attachTx(tx, ticketID, triggerID, note)
	})
}

// RequestStatus prompts an idle or waiting worker for a status report.
func (e *Engine) RequestStatus(ctx context.Context, ticketID string) (bool, error) {
	var asked bool
	err := e.repos.Transact(ctx, func(tx Tx) error {
		t, err := tx.GetTicket(ticketID)
		if err != nil || t == nil {
			return fmt.Errorf("ticket %s not found", ticketID)
		}
		if t.AssignmentID == "" {
			return nil
		}
		as, err := tx.GetAssignment(t.AssignmentID)
		if err != nil || as == nil || as.Status != AssignmentRunning {
			return nil
		}
		in := &Input{
			ID:        GenerateID("in"),
			TicketID:  t.ID,
			Kind:      InputText,
			Text:      "Please provide a concise status report of current progress.",
			Status:    InputPending,
			CreatedAt: e.now(),
		}
		if err := tx.SaveInput(in); err != nil {
			return err
		}
		if e.executor != nil {
			_ = e.executor.Deliver(tx, t.ID, as.ID, []string{in.ID}, true)
			in.Status = InputQueued
			_ = tx.SaveInput(in)
		}
		asked = true
		return nil
	})
	return asked, err
}

// Note delivers a fact from other work to a Ticket as context without granting authority.
func (e *Engine) Note(ctx context.Context, ticketID, text string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		t, err := tx.GetTicket(ticketID)
		if err != nil || t == nil {
			return fmt.Errorf("ticket %s not found", ticketID)
		}
		in := &Input{
			ID:        GenerateID("in"),
			TicketID:  t.ID,
			Kind:      InputText,
			Text:      text,
			Status:    InputPending,
			CreatedAt: e.now(),
		}
		if err := tx.SaveInput(in); err != nil {
			return err
		}
		if t.AssignmentID != "" && e.executor != nil {
			as, _ := tx.GetAssignment(t.AssignmentID)
			if as != nil && as.Status == AssignmentRunning {
				_ = e.executor.Deliver(tx, t.ID, as.ID, []string{in.ID}, false)
				in.Status = InputQueued
				_ = tx.SaveInput(in)
			}
		}
		return nil
	})
}

// Reply enqueues an outbound channel message via Outbox.
func (e *Engine) Reply(ctx context.Context, req ReplyPayload) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		if e.outbox != nil {
			return e.outbox.Send(tx, req)
		}
		return nil
	})
}

// =============================================================================
// EngineForExecutor
// =============================================================================

// AssignmentStarted records that the worker started.
func (e *Engine) AssignmentStarted(ctx context.Context, ticketID, assignmentID string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.assignmentStartedTx(tx, ticketID, assignmentID)
	})
}

// InputsDelivered records that inputs were delivered to the worker.
func (e *Engine) InputsDelivered(ctx context.Context, ticketID, assignmentID string, inputIDs []string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.inputsDeliveredTx(tx, ticketID, assignmentID, inputIDs)
	})
}

// InputsRead records that the worker took inputs into its history.
func (e *Engine) InputsRead(ctx context.Context, ticketID string, inputIDs []string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.inputsReadTx(tx, ticketID, inputIDs)
	})
}

// TurnEnded marks the end of a worker turn.
func (e *Engine) TurnEnded(ctx context.Context, ticketID, assignmentID string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.turnEndedTx(tx, ticketID, assignmentID)
	})
}

// Report records a worker's progress or completion report.
func (e *Engine) Report(ctx context.Context, ticketID, assignmentID string, state ReportState, summary, question string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.reportTx(tx, ticketID, assignmentID, state, summary, question)
	})
}

// HandBack lets a worker release an out-of-scope follow-up for fresh evaluation.
func (e *Engine) HandBack(ctx context.Context, ticketID, triggerID, reason string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.handBackTx(tx, ticketID, triggerID, reason)
	})
}

// DropSubscription removes a subscription to an unrelated Thread.
func (e *Engine) DropSubscription(ctx context.Context, ticketID, threadKey string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return tx.DeleteSubscription(ticketID, threadKey)
	})
}

// FollowContext follows another Thread for context only.
func (e *Engine) FollowContext(ctx context.Context, ticketID, threadKey string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return tx.SaveSubscription(&Subscription{
			TicketID:  ticketID,
			Thread:    threadKey,
			Kind:      SubContext,
			CreatedAt: e.now(),
		})
	})
}

// =============================================================================
// EngineForPeople
// =============================================================================

// StopTicket withdraws the Mandate and stops work on the Ticket.
func (e *Engine) StopTicket(ctx context.Context, ticketID, reason string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.stopTicketTx(tx, ticketID, reason)
	})
}

// RetryTicket restarts a failed or blocked Ticket.
func (e *Engine) RetryTicket(ctx context.Context, ticketID string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.retryTicketTx(tx, ticketID)
	})
}

// Reevaluate queues a stored Trigger for fresh evaluation.
func (e *Engine) Reevaluate(ctx context.Context, triggerID string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		tr, err := tx.GetTrigger(triggerID)
		if err != nil || tr == nil {
			return fmt.Errorf("trigger %s not found", triggerID)
		}
		th, err := tx.GetThread(tr.Thread)
		if err != nil || th == nil {
			return fmt.Errorf("thread %s not found", tr.Thread)
		}
		tr.Disposition = DispositionQueued
		tr.Expiries = 0
		_ = tx.SaveTrigger(tr)
		th.Queue = append(th.Queue, tr.ID)
		return tx.SaveThread(th)
	})
}

// Unsubscribe drops every subscription on a Thread.
func (e *Engine) Unsubscribe(ctx context.Context, threadKey string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return tx.DeleteSubscriptionsForThread(threadKey)
	})
}

// Dismiss dismisses an unavailable decision problem.
func (e *Engine) Dismiss(ctx context.Context, decisionID string) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		dec, err := tx.GetDecision(decisionID)
		if err != nil || dec == nil {
			return fmt.Errorf("decision %s not found", decisionID)
		}
		dec.Reason += " (dismissed by user)"
		return tx.SaveDecision(dec)
	})
}

// =============================================================================
// Passes & Background
// =============================================================================

// EvaluateDue checks for expired Evals and processes timeouts.
func (e *Engine) EvaluateDue(ctx context.Context) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.evaluateDueTx(tx)
	})
}

// Policy returns the active Policy.
func (e *Engine) Policy() Policy {
	if e.policy == nil {
		return Policy{}
	}
	return e.policy.CurrentPolicy()
}

// PolicyChanged notifies the engine that the policy changed from fromVersion to toVersion,
// invalidating active evals and notifying open tickets affected by effects.
func (e *Engine) PolicyChanged(ctx context.Context, fromVersion, toVersion string, effects map[string]PolicyEffect) error {
	return e.repos.Transact(ctx, func(tx Tx) error {
		return e.policyChangedTx(tx, fromVersion, toVersion, effects)
	})
}

// UpdatePolicy updates the active policy, computes effects on open tickets, and applies policy change transitions.
func (e *Engine) UpdatePolicy(ctx context.Context, newPolicy Policy) error {
	oldPolicy := e.policy.CurrentPolicy()
	fromVer := oldPolicy.Version()
	toVer := newPolicy.Version()

	if mut, ok := e.policy.(MutablePolicyProvider); ok {
		mut.SetPolicy(newPolicy)
	}

	if fromVer == toVer {
		return nil
	}

	return e.repos.Transact(ctx, func(tx Tx) error {
		openTickets, err := tx.ListOpenTickets()
		if err != nil {
			return err
		}
		effects := ComputePolicyEffects(openTickets, oldPolicy, newPolicy)
		return e.policyChangedTx(tx, fromVer, toVer, effects)
	})
}

// GetTicket retrieves a single Ticket by ID.
func (e *Engine) GetTicket(ctx context.Context, id string) (*Ticket, error) {
	var t *Ticket
	err := e.repos.Transact(ctx, func(tx Tx) error {
		var err error
		t, err = tx.GetTicket(id)
		return err
	})
	return t, err
}

// GetAssignment retrieves a single Assignment by ID.
func (e *Engine) GetAssignment(ctx context.Context, id string) (*Assignment, error) {
	var a *Assignment
	err := e.repos.Transact(ctx, func(tx Tx) error {
		var err error
		a, err = tx.GetAssignment(id)
		return err
	})
	return a, err
}

// FindTicketByIDOrName looks up a Ticket matching either its ID or its short Name (e.g. "task-xyz").
func (e *Engine) FindTicketByIDOrName(ctx context.Context, query string) (*Ticket, error) {
	var found *Ticket
	err := e.repos.Transact(ctx, func(tx Tx) error {
		t, err := tx.GetTicket(query)
		if err == nil && t != nil {
			found = t
			return nil
		}
		all, err := tx.ListTickets("")
		if err != nil {
			return err
		}
		for _, item := range all {
			if strings.EqualFold(item.Name, query) || strings.EqualFold(item.ID, query) {
				found = item
				return nil
			}
		}
		return nil
	})
	return found, err
}

// ListTickets returns tickets filtered by status ("open", "ended", or "" for all).
func (e *Engine) ListTickets(ctx context.Context, status TicketStatus) ([]*Ticket, error) {
	var list []*Ticket
	err := e.repos.Transact(ctx, func(tx Tx) error {
		var err error
		list, err = tx.ListTickets(status)
		return err
	})
	return list, err
}

// GetTicketDetail returns the complete TicketDetailView including stage, assignment, inputs, and subscriptions.
func (e *Engine) GetTicketDetail(ctx context.Context, ticketID string) (*TicketDetailView, error) {
	var detail *TicketDetailView
	err := e.repos.Transact(ctx, func(tx Tx) error {
		t, err := tx.GetTicket(ticketID)
		if err != nil || t == nil {
			return fmt.Errorf("ticket %s not found", ticketID)
		}
		var as *Assignment
		if t.AssignmentID != "" {
			as, _ = tx.GetAssignment(t.AssignmentID)
		}
		stage := ComputeStage(t, as)
		inputs, _ := tx.GetInputsForTicket(t.ID)
		subs, _ := tx.GetSubscriptionsForTicket(t.ID)

		detail = &TicketDetailView{
			Ticket:           t,
			Stage:            stage,
			HumanAttention:   stage.HumanAttention(),
			ActiveAssignment: as,
			Inputs:           inputs,
			Subscriptions:    subs,
		}
		return nil
	})
	return detail, err
}

// ListDecisions returns recorded decisions.
func (e *Engine) ListDecisions(ctx context.Context, outcome Outcome, limit int) ([]*Decision, error) {
	var list []*Decision
	err := e.repos.Transact(ctx, func(tx Tx) error {
		var err error
		list, err = tx.ListDecisions(outcome, limit)
		return err
	})
	return list, err
}

// GetAttention returns all tickets and undismissed unavailable evals that require human attention.
func (e *Engine) GetAttention(ctx context.Context) (*AttentionItem, error) {
	att := &AttentionItem{}
	err := e.repos.Transact(ctx, func(tx Tx) error {
		openTickets, err := tx.ListOpenTickets()
		if err != nil {
			return err
		}
		for _, t := range openTickets {
			var as *Assignment
			if t.AssignmentID != "" {
				as, _ = tx.GetAssignment(t.AssignmentID)
			}
			stage := ComputeStage(t, as)
			if stage.HumanAttention() {
				att.Tickets = append(att.Tickets, AttentionTicket{
					TicketID:      t.ID,
					Name:          t.Name,
					Title:         t.Title,
					Stage:         stage,
					Attention:     t.Attention,
					DeliveryError: t.DeliveryError,
					LastReport:    t.LastReport,
					Channels:      t.Channels,
					CreatedAt:     t.CreatedAt,
					UpdatedAt:     t.UpdatedAt,
				})
			}
		}

		// Unavailable decisions that have not been dismissed
		decisions, err := tx.ListDecisions(OutcomeUnavailable, 100)
		if err == nil {
			for _, d := range decisions {
				if !strings.Contains(d.Reason, "dismissed by user") {
					att.UnavailableEvals = append(att.UnavailableEvals, d)
				}
			}
		}
		return nil
	})
	return att, err
}

// GetOverview constructs the operator overview view.
func (e *Engine) GetOverview(ctx context.Context) (*OverviewView, error) {
	att, err := e.GetAttention(ctx)
	if err != nil {
		return nil, err
	}

	var (
		totalCount int
		openCount  int
		endedCount int
		load       ExecutorLoad
	)

	err = e.repos.Transact(ctx, func(tx Tx) error {
		all, err := tx.ListTickets("")
		if err != nil {
			return err
		}
		totalCount = len(all)
		for _, t := range all {
			if t.Live() {
				openCount++
				var as *Assignment
				if t.AssignmentID != "" {
					as, _ = tx.GetAssignment(t.AssignmentID)
				}
				stage := ComputeStage(t, as)
				switch stage {
				case StageWorking:
					load.Running++
				case StageWorkingQueued:
					load.Queued++
				case StageIdle:
					load.Idle++
				}
			} else {
				endedCount++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	pol := e.Policy()
	return &OverviewView{
		PolicyVersion: pol.Version(),
		AcceptMode:    pol.Mode(),
		Attention:     *att,
		ExecutorLoad:  load,
		TotalTickets:  totalCount,
		OpenTickets:   openCount,
		EndedTickets:  endedCount,
	}, nil
}
