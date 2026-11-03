package superagent

import (
	"context"
	"time"
)

// Tx provides atomic access to domain repositories and effect queuing.
type Tx interface {
	// Triggers
	GetTrigger(id string) (*TriggerRecord, error)
	SaveTrigger(tr *TriggerRecord) error

	// Threads
	GetThread(key string) (*Thread, error)
	SaveThread(th *Thread) error

	// Evals
	GetEval(id string) (*Eval, error)
	SaveEval(ev *Eval) error
	FindDueEvals(now time.Time) ([]*Eval, error)
	FindActiveEvals() ([]*Eval, error)
	GetActiveEvalForThread(threadKey string) (*Eval, error)

	// Decisions
	GetDecision(id string) (*Decision, error)
	SaveDecision(d *Decision) error
	GetLatestDecisionForThread(threadKey string) (*Decision, error)
	ListDecisions(outcome Outcome, limit int) ([]*Decision, error)

	// Tickets
	GetTicket(id string) (*Ticket, error)
	SaveTicket(t *Ticket) error
	ListOpenTickets() ([]*Ticket, error)
	ListTickets(status TicketStatus) ([]*Ticket, error)
	FindTicketsForThread(threadKey string) ([]*Ticket, error)

	// Assignments
	GetAssignment(id string) (*Assignment, error)
	SaveAssignment(a *Assignment) error
	GetActiveAssignment(ticketID string) (*Assignment, error)

	// Subscriptions
	GetSubscriptionsForThread(threadKey string) ([]*Subscription, error)
	GetSubscriptionsForTicket(ticketID string) ([]*Subscription, error)
	SaveSubscription(sub *Subscription) error
	DeleteSubscription(ticketID, threadKey string) error
	DeleteSubscriptionsForThread(threadKey string) error

	// Inputs
	GetInput(id string) (*Input, error)
	SaveInput(in *Input) error
	GetInputsForTicket(ticketID string) ([]*Input, error)
	GetPendingInputs(ticketID string) ([]*Input, error)

	// Effects
	EnqueueEffect(eff *Effect) error
	GetDueEffects(now time.Time, limit int) ([]*Effect, error)
	UpdateEffect(eff *Effect) error
}

// Repositories opens a transaction.
type Repositories interface {
	Transact(ctx context.Context, fn func(tx Tx) error) error
}

// SourceScope checks Source presence and configuration.
type SourceScope interface {
	// Listens reports whether the Source still listens to the given Thread key.
	Listens(thread string) bool
}

// Assessor delivers candidate trigger batches to the main orchestrator session.
type Assessor interface {
	Candidate(tx Tx, eval *Eval, triggers []*TriggerRecord, tickets []*Ticket) error
}

// Executor manages worker session lifecycles and input delivery.
type Executor interface {
	Launch(tx Tx, ticket *Ticket, assignment *Assignment, priority bool) error
	Deliver(tx Tx, ticketID, assignmentID string, inputIDs []string, priority bool) error
	End(tx Tx, ticketID, assignmentID, reason string, cancel bool) error
}

// Outbox enqueues channel replies and notifications.
type Outbox interface {
	Send(tx Tx, msg ReplyPayload) error
}

// PolicyProvider supplies the current active Policy.
type PolicyProvider interface {
	CurrentPolicy() Policy
}
