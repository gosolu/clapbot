package superagent

import (
	"fmt"
	"strings"
	"time"
)

// decideTx processes an Eval decision for a Trigger.
func (e *Engine) decideTx(tx Tx, triggerID string, outcome Outcome, reason string, matchedRules []string) (*Decision, error) {
	tr, err := tx.GetTrigger(triggerID)
	if err != nil {
		return nil, err
	}
	if tr == nil {
		return nil, fmt.Errorf("trigger %s not found", triggerID)
	}

	th, err := tx.GetThread(tr.Thread)
	if err != nil {
		return nil, err
	}
	if th == nil {
		return nil, fmt.Errorf("thread %s not found", tr.Thread)
	}

	policy := e.policy.CurrentPolicy()
	now := e.now()

	dec := &Decision{
		ID:            GenerateID("dec"),
		EvalID:        th.EvalID,
		Thread:        tr.Thread,
		TriggerID:     tr.ID,
		Outcome:       outcome,
		Reason:        reason,
		MatchedRules:  matchedRules,
		PolicyVersion: policy.Version(),
		At:            now,
	}

	switch outcome {
	case OutcomeAccept:
		// Check inferred rules against policy accept mode.
		if tr.Strength == StrengthInferred {
			if policy.Mode() == AcceptObserve {
				dec.Outcome = OutcomeIgnore
				dec.WouldAccept = true
				if err := tx.SaveDecision(dec); err != nil {
					return nil, err
				}
				tr.Disposition = DispositionIgnored
				_ = tx.SaveTrigger(tr)
				e.finishEvalTrigger(tx, th, tr.ID, dec.ID)
				return dec, nil
			}
			if policy.Mode() == AcceptOff {
				return nil, fmt.Errorf("cannot accept inferred candidate when accept_mode is off")
			}
		}

		mandate := Mandate{
			PolicyVersion:  policy.Version(),
			Scope:          tr.Scope,
			Strength:       tr.Strength,
			Basis:          tr.Basis,
			MatchedRules:   matchedRules,
			Requester:      tr.Actor,
			Source:         tr.Source,
			ReceivingScope: tr.Scope,
			Addressed:      tr.Addressed != AddressNone,
			TriggerID:      tr.ID,
		}

		title := strings.TrimSpace(tr.Text)
		if idx := strings.IndexByte(title, '\n'); idx > 0 {
			title = title[:idx]
		}
		if len([]rune(title)) > 80 {
			title = string([]rune(title)[:80]) + "..."
		}
		if title == "" {
			title = fmt.Sprintf("Request from %s", tr.Actor.Label())
		}

		ticketName := fmt.Sprintf("task-%s", GenerateID(""))
		t := &Ticket{
			ID:            GenerateID("t"),
			Name:          ticketName,
			Title:         title,
			Status:        TicketOpen,
			Mandate:       mandate,
			DecisionID:    dec.ID,
			OriginTrigger: tr.ID,
			OriginThread:  tr.Thread,
			Channels:      []ChannelAddress{tr.Channel},
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		dec.TicketID = t.ID

		as := &Assignment{
			ID:        GenerateID("as"),
			TicketID:  t.ID,
			Status:    AssignmentPending,
			CreatedAt: now,
		}
		t.AssignmentID = as.ID

		if err := tx.SaveAssignment(as); err != nil {
			return nil, err
		}
		if err := tx.SaveTicket(t); err != nil {
			return nil, err
		}

		// Initial subscription to the origin thread.
		sub := &Subscription{
			TicketID:  t.ID,
			Thread:    tr.Thread,
			Kind:      SubOrigin,
			CreatedAt: now,
		}
		if err := tx.SaveSubscription(sub); err != nil {
			return nil, err
		}

		// Deliver the trigger as the initial owned input.
		in := &Input{
			ID:        GenerateID("in"),
			TicketID:  t.ID,
			Kind:      InputTrigger,
			TriggerID: tr.ID,
			Owned:     true,
			Status:    InputPending,
			CreatedAt: now,
		}
		if err := tx.SaveInput(in); err != nil {
			return nil, err
		}

		tr.Disposition = DispositionAccepted
		tr.DeliveredTo = append(tr.DeliveredTo, t.ID)
		if err := tx.SaveTrigger(tr); err != nil {
			return nil, err
		}

		// Send initial acknowledgement.
		if e.outbox != nil {
			_ = e.outbox.Send(tx, ReplyPayload{
				TicketID: t.ID,
				Purpose:  PurposeAcknowledgement,
				Channel:  tr.Channel,
				Text:     fmt.Sprintf("Task `%s` accepted: %s", t.Name, t.Title),
			})
			t.Acknowledged = true
			_ = tx.SaveTicket(t)
		}

		// Launch the executor for the ticket.
		if e.executor != nil {
			if err := e.executor.Launch(tx, t, as, true); err != nil {
				return nil, err
			}
		}

	case OutcomeStop:
		tr.Disposition = DispositionStopped
		_ = tx.SaveTrigger(tr)
		// Stop any live ticket on this thread.
		subs, _ := tx.GetSubscriptionsForThread(tr.Thread)
		for _, s := range subs {
			_ = e.stopTicketTx(tx, s.TicketID, reason)
		}

	case OutcomeIgnore:
		tr.Disposition = DispositionIgnored
		_ = tx.SaveTrigger(tr)

	case OutcomeUnavailable:
		tr.Disposition = DispositionUnavailable
		_ = tx.SaveTrigger(tr)
	}

	if err := tx.SaveDecision(dec); err != nil {
		return nil, err
	}

	e.finishEvalTrigger(tx, th, tr.ID, dec.ID)
	return dec, nil
}

// finishEvalTrigger updates the Eval after a decision on one Trigger.
func (e *Engine) finishEvalTrigger(tx Tx, th *Thread, triggerID, decID string) {
	if th.EvalID == "" {
		return
	}
	ev, err := tx.GetEval(th.EvalID)
	if err != nil || ev == nil {
		return
	}
	if ev.Decided == nil {
		ev.Decided = map[string]string{}
	}
	ev.Decided[triggerID] = decID

	if len(ev.Undecided()) == 0 {
		ev.Status = EvalDone
		_ = tx.SaveEval(ev)
		th.EvalID = ""

		// If thread has queued triggers, start the next Eval.
		if len(th.Queue) > 0 {
			nextBatch := th.Queue
			th.Queue = nil
			nextEval := &Eval{
				ID:            GenerateID("eval"),
				Thread:        th.Key,
				TriggerIDs:    nextBatch,
				Decided:       map[string]string{},
				PolicyVersion: e.policy.CurrentPolicy().Version(),
				Status:        EvalPending,
				CreatedAt:     e.now(),
			}
			th.EvalID = nextEval.ID
			_ = tx.SaveEval(nextEval)

			if e.assessor != nil {
				var trs []*TriggerRecord
				for _, tid := range nextBatch {
					if tRec, _ := tx.GetTrigger(tid); tRec != nil {
						tRec.Disposition = DispositionEvaluating
						_ = tx.SaveTrigger(tRec)
						trs = append(trs, tRec)
					}
				}
				allTickets, _ := tx.FindTicketsForThread(th.Key)
				_ = e.assessor.Candidate(tx, nextEval, trs, allTickets)
			}
		}
	} else {
		_ = tx.SaveEval(ev)
	}
	_ = tx.SaveThread(th)
}

// attachTx routes a Trigger to an existing Ticket.
func (e *Engine) attachTx(tx Tx, ticketID, triggerID, note string) error {
	t, err := tx.GetTicket(ticketID)
	if err != nil || t == nil {
		return fmt.Errorf("ticket %s not found", ticketID)
	}
	tr, err := tx.GetTrigger(triggerID)
	if err != nil || tr == nil {
		return fmt.Errorf("trigger %s not found", triggerID)
	}

	if t.Stopped() {
		return fmt.Errorf("ticket %s was stopped by a person", ticketID)
	}

	policy := e.policy.CurrentPolicy()
	if !t.Live() {
		resumed, err := e.revalidateAndResume(tx, t, tr.Trigger, policy)
		if err != nil {
			return err
		}
		if !resumed {
			return fmt.Errorf("ticket %s mandate revoked, cannot attach", ticketID)
		}
	}

	// Add subscription if not already subscribed.
	sub := &Subscription{
		TicketID:  t.ID,
		Thread:    tr.Thread,
		Kind:      SubAttached,
		CreatedAt: e.now(),
	}
	_ = tx.SaveSubscription(sub)

	in := &Input{
		ID:        GenerateID("in"),
		TicketID:  t.ID,
		Kind:      InputTrigger,
		TriggerID: tr.ID,
		Text:      note,
		Owned:     true,
		Status:    InputPending,
		CreatedAt: e.now(),
	}
	if err := tx.SaveInput(in); err != nil {
		return err
	}

	tr.Disposition = DispositionAttached
	tr.DeliveredTo = append(tr.DeliveredTo, t.ID)
	_ = tx.SaveTrigger(tr)

	if t.AssignmentID != "" {
		as, _ := tx.GetAssignment(t.AssignmentID)
		if as != nil && as.Status == AssignmentRunning && e.executor != nil {
			_ = e.executor.Deliver(tx, t.ID, as.ID, []string{in.ID}, true)
			in.Status = InputQueued
			_ = tx.SaveInput(in)
		}
	}

	// If this trigger had an active eval, mark it attached.
	th, _ := tx.GetThread(tr.Thread)
	if th != nil && th.EvalID != "" {
		e.finishEvalTrigger(tx, th, tr.ID, "attached:"+t.ID)
	}

	return nil
}

// evaluateDueTx checks for expired Evals and records unavailable or retries them.
func (e *Engine) evaluateDueTx(tx Tx) error {
	now := e.now()
	due, err := tx.FindDueEvals(now)
	if err != nil {
		return err
	}

	for _, ev := range due {
		th, err := tx.GetThread(ev.Thread)
		if err != nil || th == nil {
			continue
		}
		for _, tid := range ev.Undecided() {
			tr, err := tx.GetTrigger(tid)
			if err != nil || tr == nil {
				continue
			}
			tr.Expiries++
			if tr.Expiries >= 3 {
				// Third expiry records unavailable (docs/002 When Eval does not finish).
				dec := &Decision{
					ID:            GenerateID("dec"),
					EvalID:        ev.ID,
					Thread:        ev.Thread,
					TriggerID:     tr.ID,
					Outcome:       OutcomeUnavailable,
					Reason:        "eval timed out after 3 retries",
					PolicyVersion: ev.PolicyVersion,
					At:            now,
				}
				_ = tx.SaveDecision(dec)
				tr.Disposition = DispositionUnavailable
				_ = tx.SaveTrigger(tr)
				e.finishEvalTrigger(tx, th, tr.ID, dec.ID)
			} else {
				// Retry delivery to the main session.
				_ = tx.SaveTrigger(tr)
				deadline := now.Add(90 * time.Second)
				ev.Deadline = &deadline
				_ = tx.SaveEval(ev)
				if e.assessor != nil {
					allTickets, _ := tx.FindTicketsForThread(th.Key)
					_ = e.assessor.Candidate(tx, ev, []*TriggerRecord{tr}, allTickets)
				}
			}
		}
	}
	return nil
}
