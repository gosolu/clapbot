package superagent

import (
	"context"
	"fmt"
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
	EvalID    string           `json:"eval_id"`
	Thread    string           `json:"thread"`
	Triggers  []*TriggerRecord `json:"triggers"`
	Tickets   []*Ticket        `json:"tickets"`
	Deadline  time.Time        `json:"deadline"`
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
