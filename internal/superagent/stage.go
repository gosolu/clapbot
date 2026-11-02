package superagent

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
