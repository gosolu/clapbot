package superagent

import (
	"fmt"
	"time"
)

// revalidateAndResume checks whether an ended Ticket may resume under the current Policy.
func (e *Engine) revalidateAndResume(tx Tx, t *Ticket, trigger Trigger, p Policy) (bool, error) {
	if t.Mandate.Withdrawn || t.Mandate.Revoked {
		return false, nil
	}
	if e.sources != nil && !e.sources.Listens(t.OriginThread) {
		return false, nil
	}

	// Basis-specific revalidation (docs/002).
	sp := p.Source(t.Mandate.Source)
	switch t.Mandate.Basis {
	case BasisFollowup:
		// Explicit follow-up passes.
	case BasisListed:
		if !sp.Listed(t.Mandate.Requester) {
			t.Mandate.Revoked = true
			t.EndReason = EndRevoked
			_ = tx.SaveTicket(t)
			return false, nil
		}
	case BasisBot:
		if !sp.ListedBot(t.Mandate.Requester) || p.Mode() != AcceptAuto {
			t.Mandate.Revoked = true
			t.EndReason = EndRevoked
			_ = tx.SaveTicket(t)
			return false, nil
		}
		for _, r := range t.Mandate.MatchedRules {
			if _, ok := p.Rule(r); !ok {
				t.Mandate.Revoked = true
				t.EndReason = EndRevoked
				_ = tx.SaveTicket(t)
				return false, nil
			}
		}
	default:
		// Table or inferred candidate.
		if t.Mandate.Strength == StrengthInferred && p.Mode() != AcceptAuto {
			t.Mandate.Revoked = true
			t.EndReason = EndRevoked
			_ = tx.SaveTicket(t)
			return false, nil
		}
	}

	// Resume the ticket.
	t.Status = TicketOpen
	t.EndedAt = nil
	t.ReleasedByHuman = true
	t.UpdatedAt = e.now()

	as := &Assignment{
		ID:        GenerateID("as"),
		TicketID:  t.ID,
		Status:    AssignmentPending,
		Resume:    true,
		CreatedAt: e.now(),
	}
	t.AssignmentID = as.ID

	if err := tx.SaveAssignment(as); err != nil {
		return false, err
	}
	if err := tx.SaveTicket(t); err != nil {
		return false, err
	}

	if e.executor != nil {
		if err := e.executor.Launch(tx, t, as, true); err != nil {
			return false, err
		}
	}
	return true, nil
}

// stopTicketTx withdraws the Ticket's Mandate and ends its active Assignment.
func (e *Engine) stopTicketTx(tx Tx, ticketID, reason string) error {
	t, err := tx.GetTicket(ticketID)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("ticket %s not found", ticketID)
	}

	now := e.now()
	t.Mandate.Withdrawn = true
	t.Status = TicketEnded
	t.EndReason = EndStopped
	t.EndedAt = &now
	t.UpdatedAt = now

	if t.AssignmentID != "" {
		as, err := tx.GetAssignment(t.AssignmentID)
		if err == nil && as != nil && as.Status != AssignmentEnded {
			as.Status = AssignmentEnded
			as.EndReason = EndStopped
			as.EndedAt = &now
			_ = tx.SaveAssignment(as)
			if e.executor != nil {
				_ = e.executor.End(tx, t.ID, as.ID, EndStopped, true)
			}
		}
	}

	if err := tx.SaveTicket(t); err != nil {
		return err
	}

	// Release follow-ups owned by this ticket that were not taken into history.
	inputs, _ := tx.GetInputsForTicket(t.ID)
	for _, in := range inputs {
		if in.Owned && in.Undelivered() {
			in.Status = InputReleased
			_ = tx.SaveInput(in)
			if in.TriggerID != "" {
				tr, _ := tx.GetTrigger(in.TriggerID)
				if tr != nil {
					tr.Disposition = DispositionQueued
					_ = tx.SaveTrigger(tr)
				}
			}
		}
	}

	if e.assessor != nil {
		e.enqueueNotice(tx, Notice{
			Kind:     NoticeTicketEnded,
			TicketID: t.ID,
			Text:     fmt.Sprintf("Ticket %s stopped: %s", t.ID, reason),
			Wake:     false,
		})
	}
	return nil
}

// retryTicketTx restarts an Assignment for a blocked, failed or undelivered Ticket.
func (e *Engine) retryTicketTx(tx Tx, ticketID string) error {
	t, err := tx.GetTicket(ticketID)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("ticket %s not found", ticketID)
	}
	if t.Stopped() {
		return fmt.Errorf("ticket %s was stopped by human", ticketID)
	}

	now := e.now()
	if t.AssignmentID != "" {
		as, _ := tx.GetAssignment(t.AssignmentID)
		if as != nil && as.Status != AssignmentEnded {
			as.Status = AssignmentEnded
			as.EndReason = "retried"
			as.EndedAt = &now
			_ = tx.SaveAssignment(as)
			if e.executor != nil {
				_ = e.executor.End(tx, t.ID, as.ID, "retried", true)
			}
		}
	}

	newAs := &Assignment{
		ID:        GenerateID("as"),
		TicketID:  t.ID,
		Status:    AssignmentPending,
		Recovery:  true,
		CreatedAt: now,
	}
	t.AssignmentID = newAs.ID
	t.Status = TicketOpen
	t.DeliveryError = ""
	t.ReleasedByHuman = true
	t.UpdatedAt = now

	if err := tx.SaveAssignment(newAs); err != nil {
		return err
	}
	if err := tx.SaveTicket(t); err != nil {
		return err
	}

	if e.executor != nil {
		return e.executor.Launch(tx, t, newAs, true)
	}
	return nil
}

// threadClosedTx marks a followed Thread closed and ends the Ticket if all follows are done.
func (e *Engine) threadClosedTx(tx Tx, threadKey, outcome string, at time.Time) error {
	subs, err := tx.GetSubscriptionsForThread(threadKey)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		t, err := tx.GetTicket(sub.TicketID)
		if err != nil || t == nil || !t.Live() {
			continue
		}
		// A Ticket ends when its opened threads/followed threads close (docs/003 Pull requests).
		t.Status = TicketEnded
		t.EndReason = outcome
		t.EndedAt = &at
		t.UpdatedAt = at

		if t.AssignmentID != "" {
			as, _ := tx.GetAssignment(t.AssignmentID)
			if as != nil && as.Status != AssignmentEnded {
				as.Status = AssignmentEnded
				as.EndReason = outcome
				as.EndedAt = &at
				_ = tx.SaveAssignment(as)
				if e.executor != nil {
					_ = e.executor.End(tx, t.ID, as.ID, outcome, false)
				}
			}
		}
		_ = tx.SaveTicket(t)

		// Send terminal notice to chat channels.
		for _, ch := range t.Channels {
			if e.outbox != nil {
				_ = e.outbox.Send(tx, ReplyPayload{
					TicketID: t.ID,
					Purpose:  PurposeNotice,
					Channel:  ch,
					Text:     fmt.Sprintf("PR for ticket %s has been %s.", t.Name, outcome),
				})
			}
		}
	}
	return nil
}
