package superagent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// GenerateID produces a short random ID with a prefix.
func GenerateID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	if prefix == "" {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b[:]))
}

// ReceiveResult is the outcome of Engine.Receive.
type ReceiveResult string

const (
	ReceiveDuplicate   ReceiveResult = "duplicate"
	ReceiveNotAdmitted ReceiveResult = "not_admitted"
	ReceiveOwned       ReceiveResult = "owned"
	ReceiveQueued      ReceiveResult = "queued"
	ReceiveEvaluating  ReceiveResult = "evaluating"
)

// processIntake handles a new incoming Trigger inside a transaction.
func (e *Engine) processIntake(tx Tx, t Trigger) (ReceiveResult, error) {
	if t.ID == "" {
		return "", fmt.Errorf("trigger id required")
	}
	if t.At.IsZero() {
		t.At = e.now()
	}

	// 1. Idempotency check.
	existing, err := tx.GetTrigger(t.ID)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return ReceiveDuplicate, nil
	}

	// 2. Fetch or create primary Thread.
	th, err := tx.GetThread(t.Thread)
	if err != nil {
		return "", err
	}
	if th == nil {
		th = &Thread{
			Key:    t.Thread,
			LastAt: t.At,
		}
	} else {
		th.LastAt = t.At
	}

	// 3. Subscription fan-out: find tickets subscribed to this Thread.
	subs, err := tx.GetSubscriptionsForThread(t.Thread)
	if err != nil {
		return "", err
	}

	rec := &TriggerRecord{
		Trigger:     t,
		ReceivedAt:  e.now(),
		Disposition: DispositionQueued,
	}

	var (
		ownedByTicket *Ticket
		deliveredTo   []string
		hasTickets    = len(subs) > 0
	)

	policy := e.policy.CurrentPolicy()

	for _, sub := range subs {
		ticket, err := tx.GetTicket(sub.TicketID)
		if err != nil || ticket == nil {
			continue
		}
		// A stopped ticket receives nothing.
		if ticket.Stopped() {
			continue
		}

		// A person's message on the Ticket's own Thread (origin or attached) belongs to that Ticket's Executor.
		isOwnThread := sub.Kind == SubOrigin || sub.Kind == SubAttached
		if !t.Actor.Bot && isOwnThread && ownedByTicket == nil {
			// If ticket is ended, try to resume it.
			if !ticket.Live() {
				resumed, err := e.revalidateAndResume(tx, ticket, t, policy)
				if err != nil {
					return "", err
				}
				if !resumed {
					// Revalidation failed: Mandate revoked, ticket stays ended.
					continue
				}
			}

			// Deliver as owned Input to this ticket.
			ownedByTicket = ticket
			deliveredTo = append(deliveredTo, ticket.ID)
			in := &Input{
				ID:        GenerateID("in"),
				TicketID:  ticket.ID,
				Kind:      InputTrigger,
				TriggerID: t.ID,
				Owned:     true,
				Status:    InputPending,
				CreatedAt: e.now(),
			}
			if err := tx.SaveInput(in); err != nil {
				return "", err
			}
			ticket.AnswerChannel = &t.Channel
			ticket.UpdatedAt = e.now()
			if err := tx.SaveTicket(ticket); err != nil {
				return "", err
			}

			// Wake worker session if assigned.
			if ticket.AssignmentID != "" {
				as, err := tx.GetAssignment(ticket.AssignmentID)
				if err == nil && as != nil && as.Status == AssignmentRunning {
					if err := e.executor.Deliver(tx, ticket.ID, as.ID, []string{in.ID}, true); err != nil {
						return "", err
					}
					in.Status = InputQueued
					_ = tx.SaveInput(in)
				}
			}

			// Main session receives a follow-up notice.
			if e.assessor != nil {
				notice := Notice{
					Kind:     NoticeFollowUp,
					TicketID: ticket.ID,
					Text:     fmt.Sprintf("[Follow-up from %s on ticket %s]\n%s", t.Actor.Label(), ticket.ID, t.Text),
					Wake:     false,
				}
				e.enqueueNotice(tx, notice)
			}
		} else {
			// Ordinary context fan-out.
			deliveredTo = append(deliveredTo, ticket.ID)
			inStatus := InputPending
			if ticket.Halted() && t.Actor.Bot {
				inStatus = InputHeld
			}
			in := &Input{
				ID:        GenerateID("in"),
				TicketID:  ticket.ID,
				Kind:      InputTrigger,
				TriggerID: t.ID,
				Owned:     false,
				Status:    inStatus,
				CreatedAt: e.now(),
			}
			if err := tx.SaveInput(in); err != nil {
				return "", err
			}
			if inStatus == InputPending && ticket.AssignmentID != "" {
				as, _ := tx.GetAssignment(ticket.AssignmentID)
				if as != nil && as.Status == AssignmentRunning {
					_ = e.executor.Deliver(tx, ticket.ID, as.ID, []string{in.ID}, false)
					in.Status = InputQueued
					_ = tx.SaveInput(in)
				}
			}
		}
	}

	rec.DeliveredTo = deliveredTo

	// If a Ticket owns this message, it takes no Eval.
	if ownedByTicket != nil {
		rec.Disposition = DispositionOwned
		if err := tx.SaveTrigger(rec); err != nil {
			return "", err
		}
		if err := tx.SaveThread(th); err != nil {
			return "", err
		}
		return ReceiveOwned, nil
	}

	// 4. Gate check for Eval.
	strength, basis := policy.Gate(t, hasTickets)
	rec.Strength = strength
	rec.Basis = basis

	if strength == StrengthNone {
		rec.Disposition = DispositionNotAdmitted
		if err := tx.SaveTrigger(rec); err != nil {
			return "", err
		}
		if err := tx.SaveThread(th); err != nil {
			return "", err
		}
		return ReceiveNotAdmitted, nil
	}

	// 5. Admitted to Eval.
	if th.EvalID != "" {
		// Thread already has an active Eval; batch trigger into queue.
		th.Queue = append(th.Queue, t.ID)
		rec.Disposition = DispositionQueued
		if err := tx.SaveTrigger(rec); err != nil {
			return "", err
		}
		if err := tx.SaveThread(th); err != nil {
			return "", err
		}
		return ReceiveQueued, nil
	}

	// Start new Eval on this Thread.
	eval := &Eval{
		ID:            GenerateID("eval"),
		Thread:        th.Key,
		TriggerIDs:    []string{t.ID},
		Decided:       map[string]string{},
		PolicyVersion: policy.Version(),
		Status:        EvalPending,
		CreatedAt:     e.now(),
	}
	th.EvalID = eval.ID
	rec.Disposition = DispositionEvaluating

	if err := tx.SaveEval(eval); err != nil {
		return "", err
	}
	if err := tx.SaveTrigger(rec); err != nil {
		return "", err
	}
	if err := tx.SaveThread(th); err != nil {
		return "", err
	}

	// Relay candidate to the main orchestrator session.
	if e.assessor != nil {
		allTickets, _ := tx.FindTicketsForThread(th.Key)
		if err := e.assessor.Candidate(tx, eval, []*TriggerRecord{rec}, allTickets); err != nil {
			return "", err
		}
	}

	return ReceiveEvaluating, nil
}

func (e *Engine) enqueueNotice(tx Tx, n Notice) {
	raw, err := json.Marshal(n)
	if err != nil {
		return
	}
	eff := &Effect{
		Kind:      EffectRelay,
		Lane:      "main_session",
		Payload:   raw,
		Status:    EffectPendingStatus,
		CreatedAt: e.now(),
		NextAt:    e.now(),
	}
	_ = tx.EnqueueEffect(eff)
}
