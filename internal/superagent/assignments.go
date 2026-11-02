package superagent

import (
	"fmt"
)

// assignmentStartedTx records that an Assignment started executing.
func (e *Engine) assignmentStartedTx(tx Tx, ticketID, assignmentID string) error {
	as, err := tx.GetAssignment(assignmentID)
	if err != nil {
		return err
	}
	if as == nil {
		return fmt.Errorf("assignment %s not found", assignmentID)
	}

	now := e.now()
	as.Status = AssignmentRunning
	as.StartedAt = &now
	if err := tx.SaveAssignment(as); err != nil {
		return err
	}

	// Deliver any pending inputs to the newly running Assignment.
	pending, err := tx.GetPendingInputs(ticketID)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		var inputIDs []string
		for _, in := range pending {
			inputIDs = append(inputIDs, in.ID)
			in.Status = InputQueued
			in.AssignmentID = as.ID
			_ = tx.SaveInput(in)
		}
		if e.executor != nil {
			if err := e.executor.Deliver(tx, ticketID, as.ID, inputIDs, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// inputsDeliveredTx marks Inputs as delivered to the Executor session.
func (e *Engine) inputsDeliveredTx(tx Tx, ticketID, assignmentID string, inputIDs []string) error {
	for _, id := range inputIDs {
		in, err := tx.GetInput(id)
		if err != nil || in == nil {
			continue
		}
		in.Status = InputDelivered
		in.AssignmentID = assignmentID
		_ = tx.SaveInput(in)
	}
	t, _ := tx.GetTicket(ticketID)
	if t != nil && t.DeliveryError != "" {
		t.DeliveryError = ""
		_ = tx.SaveTicket(t)
	}
	return nil
}

// inputsReadTx records that the worker took these Inputs into history.
func (e *Engine) inputsReadTx(tx Tx, ticketID string, inputIDs []string) error {
	for _, id := range inputIDs {
		in, err := tx.GetInput(id)
		if err != nil || in == nil {
			continue
		}
		in.Status = InputRead
		_ = tx.SaveInput(in)
	}
	return nil
}

// turnEndedTx marks the worker's turn finished.
func (e *Engine) turnEndedTx(tx Tx, ticketID, assignmentID string) error {
	as, err := tx.GetAssignment(assignmentID)
	if err != nil || as == nil {
		return nil
	}
	as.Busy = false
	return tx.SaveAssignment(as)
}

// reportTx records an Executor's structured state report.
func (e *Engine) reportTx(tx Tx, ticketID, assignmentID string, state ReportState, summary, question string) error {
	t, err := tx.GetTicket(ticketID)
	if err != nil || t == nil {
		return fmt.Errorf("ticket %s not found", ticketID)
	}

	now := e.now()
	rep := &Report{
		ID:           GenerateID("rep"),
		TicketID:     ticketID,
		AssignmentID: assignmentID,
		State:        state,
		Summary:      summary,
		Question:     question,
		At:           now,
	}
	t.LastReport = rep
	t.UpdatedAt = now

	if question != "" {
		t.Attention = question
		// Deliver question to originating conversation immediately via Outbox (docs/000).
		targetChan := t.OriginatingChannel()
		if e.outbox != nil {
			_ = e.outbox.Send(tx, ReplyPayload{
				TicketID: t.ID,
				Purpose:  PurposeAttention,
				Channel:  targetChan,
				Text:     fmt.Sprintf("⚠️ **Question for Ticket %s**:\n%s", t.Name, question),
			})
		}
		if e.assessor != nil {
			e.enqueueNotice(tx, Notice{
				Kind:     NoticeWorkerAttention,
				TicketID: t.ID,
				Text:     fmt.Sprintf("[Attention needed on ticket %s]\n%s", t.ID, question),
				Wake:     true,
			})
		}
	} else if t.Attention != "" {
		// Clear prior attention if no longer asking.
		t.Attention = ""
	}

	switch state {
	case ReportBlocked, ReportFailed:
		t.ReleasedByHuman = false
	case ReportReady:
		// Ready without pull request ends ticket (docs/003).
		t.Status = TicketEnded
		t.EndReason = EndCompleted
		t.EndedAt = &now
		if e.outbox != nil {
			targetChan := t.OriginatingChannel()
			_ = e.outbox.Send(tx, ReplyPayload{
				TicketID: t.ID,
				Purpose:  PurposeResult,
				Channel:  targetChan,
				Text:     fmt.Sprintf("✅ **Ticket %s complete**:\n%s", t.Name, summary),
			})
		}
	case ReportDropped:
		t.Status = TicketEnded
		t.EndReason = EndDropped
		t.EndedAt = &now
	}

	if err := tx.SaveTicket(t); err != nil {
		return err
	}

	// Relay report to main orchestrator session.
	if e.assessor != nil && question == "" {
		e.enqueueNotice(tx, Notice{
			Kind:     NoticeWorkerReport,
			TicketID: t.ID,
			Text:     fmt.Sprintf("[Worker Report for %s: %s]\n%s", t.ID, state, summary),
			Wake:     false,
		})
	}
	return nil
}

// OriginatingChannel returns the first Channel or default.
func (t *Ticket) OriginatingChannel() ChannelAddress {
	if t.AnswerChannel != nil {
		return *t.AnswerChannel
	}
	if len(t.Channels) > 0 {
		return t.Channels[0]
	}
	return ChannelAddress{}
}

// handBackTx processes a worker handing back a message beyond its scope.
func (e *Engine) handBackTx(tx Tx, ticketID, triggerID, reason string) error {
	tr, err := tx.GetTrigger(triggerID)
	if err != nil || tr == nil {
		return fmt.Errorf("trigger %s not found", triggerID)
	}

	tr.HandedBack = append(tr.HandedBack, HandBack{
		TicketID: ticketID,
		Reason:   reason,
	})

	// Release ownership so it can proceed to a fresh Eval.
	th, err := tx.GetThread(tr.Thread)
	if err != nil || th == nil {
		return tx.SaveTrigger(tr)
	}

	tr.Disposition = DispositionQueued
	if err := tx.SaveTrigger(tr); err != nil {
		return err
	}

	th.Queue = append(th.Queue, tr.ID)
	return tx.SaveThread(th)
}
