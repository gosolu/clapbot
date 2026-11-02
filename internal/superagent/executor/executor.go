package executor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gosolu/clapbot/internal/superagent"
)

var _ superagent.Executor = (*TransactionalExecutor)(nil)

// TransactionalExecutor records worker execution effects inside the database transaction.
type TransactionalExecutor struct {
	now func() time.Time
}

// NewTransactionalExecutor returns a superagent.Executor that enqueues effects.
func NewTransactionalExecutor(now func() time.Time) *TransactionalExecutor {
	if now == nil {
		now = time.Now
	}
	return &TransactionalExecutor{now: now}
}

// Launch enqueues an EffectLaunch.
func (e *TransactionalExecutor) Launch(tx superagent.Tx, ticket *superagent.Ticket, assignment *superagent.Assignment, priority bool) error {
	payload := superagent.LaunchPayload{
		TicketID:     ticket.ID,
		AssignmentID: assignment.ID,
		Recovery:     assignment.Recovery,
		Resume:       assignment.Resume,
		Priority:     priority,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal launch payload: %w", err)
	}

	return tx.EnqueueEffect(&superagent.Effect{
		Kind:      superagent.EffectLaunch,
		Lane:      "worker:" + ticket.ID,
		Payload:   raw,
		Status:    superagent.EffectPendingStatus,
		CreatedAt: e.now(),
		NextAt:    e.now(),
	})
}

// Deliver enqueues an EffectDeliver.
func (e *TransactionalExecutor) Deliver(tx superagent.Tx, ticketID, assignmentID string, inputIDs []string, priority bool) error {
	payload := superagent.DeliverPayload{
		TicketID:     ticketID,
		AssignmentID: assignmentID,
		InputIDs:     inputIDs,
		Priority:     priority,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal deliver payload: %w", err)
	}

	return tx.EnqueueEffect(&superagent.Effect{
		Kind:      superagent.EffectDeliver,
		Lane:      "worker:" + ticketID,
		Payload:   raw,
		Status:    superagent.EffectPendingStatus,
		CreatedAt: e.now(),
		NextAt:    e.now(),
	})
}

// End enqueues an EffectEnd.
func (e *TransactionalExecutor) End(tx superagent.Tx, ticketID, assignmentID, reason string, cancel bool) error {
	payload := superagent.EndPayload{
		TicketID:     ticketID,
		AssignmentID: assignmentID,
		Reason:       reason,
		Cancel:       cancel,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal end payload: %w", err)
	}

	return tx.EnqueueEffect(&superagent.Effect{
		Kind:      superagent.EffectEnd,
		Lane:      "worker:" + ticketID,
		Payload:   raw,
		Status:    superagent.EffectPendingStatus,
		CreatedAt: e.now(),
		NextAt:    e.now(),
	})
}
