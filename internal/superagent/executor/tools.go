package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/react"
)

const (
	ContextKeyTicketID     = "superagent.ticket_id"
	ContextKeyAssignmentID = "superagent.assignment_id"
)

// ReportArgs defines the input for the worker_report tool.
type ReportArgs struct {
	State    string `json:"state" desc:"Worker state: progress | waiting | ready | blocked | failed | dropped" required:"true"`
	Summary  string `json:"summary" desc:"Concise summary of progress, outcome, or blocker" required:"true"`
	Question string `json:"question,omitempty" desc:"One concrete question that requires the ticket owner's decision"`
}

// ReplyArgs defines the input for the worker_reply tool.
type ReplyArgs struct {
	Purpose string `json:"purpose" desc:"Message purpose: answer | result | attention" required:"true"`
	Text    string `json:"text" desc:"Message text to deliver to the originating conversation" required:"true"`
}

// HandbackArgs defines the input for the worker_handback tool.
type HandbackArgs struct {
	TriggerID string `json:"trigger_id" desc:"ID of the trigger message that is beyond this ticket's scope" required:"true"`
	Reason    string `json:"reason" desc:"Why this request cannot be handled by this ticket" required:"true"`
}

// SubscribeContextArgs defines the input for the worker_subscribe_context tool.
type SubscribeContextArgs struct {
	ThreadKey string `json:"thread_key" desc:"External Thread key (e.g. an Issue or PR thread) to follow for context" required:"true"`
}

// NewWorkerTools builds the tools provided to worker sessions.
func NewWorkerTools(eng *superagent.Engine) []react.Tool {
	reportTool := react.NewTool("worker_report",
		"Reports your current structured state on this ticket (progress, waiting, ready, blocked, failed, dropped). Use question only when you need a concrete decision from the user.",
		func(ctx context.Context, in ReportArgs) (string, error) {
			rc := react.RunContextFrom(ctx)
			if rc == nil {
				return "", fmt.Errorf("no run context")
			}
			tID, _ := rc.Get(ContextKeyTicketID)
			aID, _ := rc.Get(ContextKeyAssignmentID)
			ticketID, _ := tID.(string)
			assignmentID, _ := aID.(string)
			if ticketID == "" || assignmentID == "" {
				return "", fmt.Errorf("worker run context missing ticket_id or assignment_id")
			}

			st := superagent.ReportState(strings.ToLower(strings.TrimSpace(in.State)))
			if !st.Valid() {
				return "", fmt.Errorf("invalid report state %q; must be one of: progress, waiting, ready, blocked, failed, dropped", in.State)
			}

			if err := eng.Report(ctx, ticketID, assignmentID, st, in.Summary, in.Question); err != nil {
				return "", fmt.Errorf("report failed: %w", err)
			}
			return fmt.Sprintf("Report recorded: state=%s", st), nil
		},
	)

	replyTool := react.NewTool("worker_reply",
		"Sends an answer, result, or question directly to the user in the ticket's originating chat or PR conversation.",
		func(ctx context.Context, in ReplyArgs) (string, error) {
			rc := react.RunContextFrom(ctx)
			if rc == nil {
				return "", fmt.Errorf("no run context")
			}
			tID, _ := rc.Get(ContextKeyTicketID)
			ticketID, _ := tID.(string)
			if ticketID == "" {
				return "", fmt.Errorf("worker run context missing ticket_id")
			}

			purp := strings.ToLower(strings.TrimSpace(in.Purpose))
			if purp != superagent.PurposeAnswer && purp != superagent.PurposeResult && purp != superagent.PurposeAttention {
				return "", fmt.Errorf("invalid purpose %q; must be answer, result, or attention", in.Purpose)
			}

			err := eng.Reply(ctx, superagent.ReplyPayload{
				TicketID: ticketID,
				Purpose:  purp,
				Text:     in.Text,
			})
			if err != nil {
				return "", fmt.Errorf("reply failed: %w", err)
			}
			return "Reply queued for delivery", nil
		},
	)

	handbackTool := react.NewTool("worker_handback",
		"Hands back a follow-up message that is beyond this ticket's scope or belongs to another ticket, so the router can evaluate it independently.",
		func(ctx context.Context, in HandbackArgs) (string, error) {
			rc := react.RunContextFrom(ctx)
			if rc == nil {
				return "", fmt.Errorf("no run context")
			}
			tID, _ := rc.Get(ContextKeyTicketID)
			ticketID, _ := tID.(string)
			if ticketID == "" {
				return "", fmt.Errorf("worker run context missing ticket_id")
			}

			err := eng.HandBack(ctx, ticketID, in.TriggerID, in.Reason)
			if err != nil {
				return "", fmt.Errorf("handback failed: %w", err)
			}
			return fmt.Sprintf("Trigger %s handed back to router", in.TriggerID), nil
		},
	)

	subscribeContextTool := react.NewTool("worker_subscribe_context",
		"Subscribes to another thread (e.g. an Issue or PR) for context only. Subsequent updates on that thread will be delivered to your session.",
		func(ctx context.Context, in SubscribeContextArgs) (string, error) {
			rc := react.RunContextFrom(ctx)
			if rc == nil {
				return "", fmt.Errorf("no run context")
			}
			tID, _ := rc.Get(ContextKeyTicketID)
			ticketID, _ := tID.(string)
			if ticketID == "" {
				return "", fmt.Errorf("worker run context missing ticket_id")
			}

			err := eng.FollowContext(ctx, ticketID, in.ThreadKey)
			if err != nil {
				return "", fmt.Errorf("subscribe context failed: %w", err)
			}
			return fmt.Sprintf("Now following thread %s for context", in.ThreadKey), nil
		},
	)

	return []react.Tool{reportTool, replyTool, handbackTool, subscribeContextTool}
}
