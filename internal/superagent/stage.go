package superagent

import "time"

// Stage names the current stage of an open Ticket for display and attention filtering.
type Stage string

const (
	StagePersonDelivery Stage = "person/delivery"
	StagePersonAnswer   Stage = "person/answer"
	StagePersonReview   Stage = "person/review"
	StagePersonBlocked  Stage = "person/blocked"
	StagePersonFailed   Stage = "person/failed"
	StageWorkingQueued  Stage = "working/queued"
	StageIdle           Stage = "idle"
	StageWorking        Stage = "working"
)

// HumanAttention reports whether the Ticket is blocked waiting for human input.
func (s Stage) HumanAttention() bool {
	switch s {
	case StagePersonDelivery, StagePersonAnswer, StagePersonReview, StagePersonBlocked, StagePersonFailed:
		return true
	}
	return false
}

// ComputeStage derives the Ticket's current stage based on its state and active assignment.
// See docs/003 Ticket stages: evaluated top-to-bottom; first matching condition wins.
func ComputeStage(t *Ticket, a *Assignment) Stage {
	if t.DeliveryError != "" {
		return StagePersonDelivery
	}
	if t.Attention != "" {
		return StagePersonAnswer
	}
	if t.LastReport != nil && t.LastReport.State == ReportReady {
		return StagePersonReview
	}
	if t.LastReport != nil && !t.ReleasedByHuman {
		if t.LastReport.State == ReportBlocked {
			return StagePersonBlocked
		}
		if t.LastReport.State == ReportFailed {
			return StagePersonFailed
		}
	}
	if t.AssignmentID == "" || (a != nil && a.Status == AssignmentPending) {
		return StageWorkingQueued
	}
	if (t.LastReport != nil && t.LastReport.State == ReportWaiting) || (a != nil && a.Status == AssignmentEnded) {
		return StageIdle
	}
	return StageWorking
}

// AttentionTicket describes a ticket requiring human action.
type AttentionTicket struct {
	TicketID      string           `json:"ticket_id"`
	Name          string           `json:"name"`
	Title         string           `json:"title"`
	Stage         Stage            `json:"stage"`
	Attention     string           `json:"attention,omitempty"`
	DeliveryError string           `json:"delivery_error,omitempty"`
	LastReport    *Report          `json:"last_report,omitempty"`
	Channels      []ChannelAddress `json:"channels,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// AttentionItem aggregates tickets and decisions requiring human action.
type AttentionItem struct {
	Tickets          []AttentionTicket `json:"tickets"`
	UnavailableEvals []*Decision       `json:"unavailable_evals"`
}

// TicketSummaryView is a summary row of a ticket for list endpoints.
type TicketSummaryView struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Title          string           `json:"title"`
	Status         TicketStatus     `json:"status"`
	Stage          Stage            `json:"stage"`
	HumanAttention bool             `json:"human_attention"`
	Attention      string           `json:"attention,omitempty"`
	AssignmentID   string           `json:"assignment_id,omitempty"`
	Requester      string           `json:"requester,omitempty"`
	Channels       []ChannelAddress `json:"channels,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// TicketDetailView is the full view of a ticket including inputs and subscriptions.
type TicketDetailView struct {
	Ticket           *Ticket         `json:"ticket"`
	Stage            Stage           `json:"stage"`
	HumanAttention   bool            `json:"human_attention"`
	ActiveAssignment *Assignment     `json:"active_assignment,omitempty"`
	Inputs           []*Input        `json:"inputs"`
	Subscriptions    []*Subscription `json:"subscriptions"`
}

// OverviewView represents system health, load, flow, and attention.
type OverviewView struct {
	PolicyVersion string        `json:"policy_version"`
	AcceptMode    AcceptMode    `json:"accept_mode"`
	Attention     AttentionItem `json:"attention"`
	ExecutorLoad  ExecutorLoad  `json:"executor_load"`
	TotalTickets  int           `json:"total_tickets"`
	OpenTickets   int           `json:"open_tickets"`
	EndedTickets  int           `json:"ended_tickets"`
}

// ExecutorLoad captures current worker load.
type ExecutorLoad struct {
	Running  int `json:"running"`
	Queued   int `json:"queued"`
	Idle     int `json:"idle"`
	MaxSlots int `json:"max_slots,omitempty"`
}
