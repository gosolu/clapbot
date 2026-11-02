package orchestrator

import (
	"fmt"
	"strings"

	"github.com/gosolu/clapbot/internal/superagent"
)

// RenderCandidateCard produces the orchestrator intake card for an Eval candidate batch.
func RenderCandidateCard(eval *superagent.Eval, triggers []*superagent.TriggerRecord, tickets []*superagent.Ticket) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📋 **Candidate Batch** (Eval: `%s`, Thread: `%s`)\n", eval.ID, eval.Thread))

	if len(tickets) > 0 {
		b.WriteString("Existing tickets on thread:\n")
		for _, t := range tickets {
			b.WriteString(fmt.Sprintf("- Ticket `%s` (%s): status=%s\n", t.ID, t.Name, t.Status))
		}
	} else {
		b.WriteString("No existing tickets on this thread.\n")
	}

	b.WriteString("\nTriggers to evaluate:\n")
	for i, tr := range triggers {
		b.WriteString(fmt.Sprintf("\n[%d] Trigger ID: `%s`\n", i+1, tr.ID))
		b.WriteString(fmt.Sprintf("From: %s (Bot: %t)\n", tr.Actor.Label(), tr.Actor.Bot))
		b.WriteString(fmt.Sprintf("Strength: `%s` | Basis: `%s` | Addressed: `%s`\n", tr.Strength, tr.Basis, tr.Addressed))
		b.WriteString(fmt.Sprintf("Message:\n\"\"\"\n%s\n\"\"\"\n", strings.TrimSpace(tr.Text)))
	}

	b.WriteString("\nAction: Call `orchestrator_route` to accept, attach to an existing ticket, stop, or ignore.")
	return b.String()
}

// RenderFollowUpCard formats a follow-up notice when a person writes on a thread with tickets.
func RenderFollowUpCard(ticket *superagent.Ticket, tr *superagent.Trigger) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("ℹ️ **Follow-up on Ticket %s** (`%s`)\n", ticket.Name, ticket.ID))
	b.WriteString(fmt.Sprintf("Sender: %s\n", tr.Actor.Label()))
	b.WriteString(fmt.Sprintf("Content: %s\n", tr.Text))
	b.WriteString("\nReminder: This message already reached the ticket's worker. You may act on a follow-up only to STOP work if the person explicitly requests a stop.")
	return b.String()
}

// RenderReportCard formats a worker report notice.
func RenderReportCard(ticket *superagent.Ticket, rep *superagent.Report) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 **Worker Report for Ticket %s** (`%s`)\n", ticket.Name, ticket.ID))
	b.WriteString(fmt.Sprintf("State: `%s`\n", rep.State))
	b.WriteString(fmt.Sprintf("Summary: %s\n", rep.Summary))
	if rep.Question != "" {
		b.WriteString(fmt.Sprintf("⚠️ Question for User: %s\n", rep.Question))
	}
	return b.String()
}

// RenderAttentionCard formats an urgent question requiring human attention.
func RenderAttentionCard(ticket *superagent.Ticket, question string) string {
	return fmt.Sprintf("⚠️ **Human Attention Needed on Ticket %s** (`%s`)\nQuestion: %s", ticket.Name, ticket.ID, question)
}
