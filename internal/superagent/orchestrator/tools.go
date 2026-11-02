package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/react"
)

// RouteAction describes a single routing decision in orchestrator_route.
type RouteAction struct {
	Kind         string   `json:"kind" desc:"Action kind: accept | attach | stop | ignore" required:"true"`
	TriggerID    string   `json:"trigger_id" desc:"ID of the trigger being decided" required:"true"`
	TicketID     string   `json:"ticket_id,omitempty" desc:"Ticket ID (required for attach)"`
	Reason       string   `json:"reason,omitempty" desc:"Brief reason for this routing decision"`
	MatchedRules []string `json:"matched_rules,omitempty" desc:"Names of matched proactive rules (for accept)"`
	Note         string   `json:"note,omitempty" desc:"Routing note (max 30 words) for attach or accept"`
}

// RouteArgs is the argument struct for orchestrator_route.
type RouteArgs struct {
	Actions []RouteAction `json:"actions" desc:"List of routing actions to execute" required:"true"`
}

// ReplyArgs is the argument struct for orchestrator_reply.
type ReplyArgs struct {
	TicketID string `json:"ticket_id,omitempty" desc:"Ticket ID if replying to a ticket's conversation"`
	Purpose  string `json:"purpose" desc:"Message purpose: attention | notice" required:"true"`
	Text     string `json:"text" desc:"Text content to deliver" required:"true"`
}

// NoteArgs is the argument struct for orchestrator_ticket_note.
type NoteArgs struct {
	TicketID string `json:"ticket_id" desc:"Target ticket ID" required:"true"`
	Text     string `json:"text" desc:"Fact or observation from other work to provide as context" required:"true"`
}

// RequestStatusArgs is the argument struct for orchestrator_ticket_request_status.
type RequestStatusArgs struct {
	TicketID string `json:"ticket_id" desc:"Ticket ID to query for status" required:"true"`
}

// RetryArgs is the argument struct for orchestrator_ticket_retry.
type RetryArgs struct {
	TicketID string `json:"ticket_id" desc:"Ticket ID to restart after a failure or block" required:"true"`
}

// UnsubscribeArgs is the argument struct for orchestrator_thread_unsubscribe.
type UnsubscribeArgs struct {
	ThreadKey string `json:"thread_key" desc:"Thread key to unsubscribe from" required:"true"`
}

// NewOrchestratorTools constructs the management tools exposed to the main orchestrator agent.
func NewOrchestratorTools(eng *superagent.Engine) []react.Tool {
	routeTool := react.NewTool("orchestrator_route",
		"Processes one or more routing decisions (accept, attach, stop, ignore) for incoming trigger candidates.",
		func(ctx context.Context, in RouteArgs) (string, error) {
			if len(in.Actions) == 0 {
				return "No actions provided", nil
			}
			var results []string
			for _, act := range in.Actions {
				kind := strings.ToLower(strings.TrimSpace(act.Kind))
				switch kind {
				case "accept":
					dec, err := eng.Decide(ctx, act.TriggerID, superagent.OutcomeAccept, act.Reason, act.MatchedRules)
					if err != nil {
						results = append(results, fmt.Sprintf("accept %s failed: %v", act.TriggerID, err))
					} else if dec.WouldAccept {
						results = append(results, fmt.Sprintf("trigger %s observed (would accept under auto mode)", act.TriggerID))
					} else {
						results = append(results, fmt.Sprintf("trigger %s accepted -> ticket %s", act.TriggerID, dec.TicketID))
					}
				case "attach":
					if act.TicketID == "" {
						results = append(results, fmt.Sprintf("attach %s failed: ticket_id required", act.TriggerID))
						continue
					}
					err := eng.Attach(ctx, act.TicketID, act.TriggerID, act.Note)
					if err != nil {
						results = append(results, fmt.Sprintf("attach %s to %s failed: %v", act.TriggerID, act.TicketID, err))
					} else {
						results = append(results, fmt.Sprintf("trigger %s attached to ticket %s", act.TriggerID, act.TicketID))
					}
				case "stop":
					dec, err := eng.Decide(ctx, act.TriggerID, superagent.OutcomeStop, act.Reason, nil)
					if err != nil {
						results = append(results, fmt.Sprintf("stop %s failed: %v", act.TriggerID, err))
					} else {
						results = append(results, fmt.Sprintf("trigger %s stopped (decision: %s)", act.TriggerID, dec.ID))
					}
				case "ignore":
					_, err := eng.Decide(ctx, act.TriggerID, superagent.OutcomeIgnore, act.Reason, nil)
					if err != nil {
						results = append(results, fmt.Sprintf("ignore %s failed: %v", act.TriggerID, err))
					} else {
						results = append(results, fmt.Sprintf("trigger %s ignored", act.TriggerID))
					}
				default:
					results = append(results, fmt.Sprintf("unknown action kind: %q", act.Kind))
				}
			}
			return strings.Join(results, "\n"), nil
		},
	)

	replyTool := react.NewTool("orchestrator_reply",
		"Sends an attention question or notice to the user in a ticket's conversation.",
		func(ctx context.Context, in ReplyArgs) (string, error) {
			purp := strings.ToLower(strings.TrimSpace(in.Purpose))
			if purp != superagent.PurposeAttention && purp != superagent.PurposeNotice {
				return "", fmt.Errorf("invalid purpose %q; must be attention or notice", in.Purpose)
			}
			err := eng.Reply(ctx, superagent.ReplyPayload{
				TicketID: in.TicketID,
				Purpose:  purp,
				Text:     in.Text,
			})
			if err != nil {
				return "", fmt.Errorf("reply failed: %w", err)
			}
			return "Message queued for delivery", nil
		},
	)

	noteTool := react.NewTool("orchestrator_ticket_note",
		"Provides an external fact or observation to a ticket's worker without directing implementation.",
		func(ctx context.Context, in NoteArgs) (string, error) {
			if err := eng.Note(ctx, in.TicketID, in.Text); err != nil {
				return "", fmt.Errorf("note failed: %w", err)
			}
			return fmt.Sprintf("Note delivered to ticket %s", in.TicketID), nil
		},
	)

	statusTool := react.NewTool("orchestrator_ticket_request_status",
		"Asks a waiting or idle worker to provide a concise status report.",
		func(ctx context.Context, in RequestStatusArgs) (string, error) {
			asked, err := eng.RequestStatus(ctx, in.TicketID)
			if err != nil {
				return "", fmt.Errorf("request status failed: %w", err)
			}
			if !asked {
				return fmt.Sprintf("Ticket %s has no active worker running", in.TicketID), nil
			}
			return fmt.Sprintf("Status request sent to ticket %s", in.TicketID), nil
		},
	)

	retryTool := react.NewTool("orchestrator_ticket_retry",
		"Restarts an assignment for a ticket that is blocked or failed.",
		func(ctx context.Context, in RetryArgs) (string, error) {
			if err := eng.RetryTicket(ctx, in.TicketID); err != nil {
				return "", fmt.Errorf("retry ticket failed: %w", err)
			}
			return fmt.Sprintf("Ticket %s restarted", in.TicketID), nil
		},
	)

	unsubscribeTool := react.NewTool("orchestrator_thread_unsubscribe",
		"Drops all subscriptions on a thread so no further messages from it are followed.",
		func(ctx context.Context, in UnsubscribeArgs) (string, error) {
			if err := eng.Unsubscribe(ctx, in.ThreadKey); err != nil {
				return "", fmt.Errorf("unsubscribe failed: %w", err)
			}
			return fmt.Sprintf("Unsubscribed from thread %s", in.ThreadKey), nil
		},
	)

	return []react.Tool{
		routeTool,
		replyTool,
		noteTool,
		statusTool,
		retryTool,
		unsubscribeTool,
	}
}
