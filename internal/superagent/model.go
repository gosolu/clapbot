// Package superagent implements the domain core of Clapbot's super-agent mode, modelled on
// deepseek-ai/dsh-superagent-prototype: Sources produce Triggers, Triggers form Threads, an
// Eval (gate + assessment) decides whether a batch of Triggers asks for work, an accept creates
// a Ticket with an immutable Mandate, and an Assignment hands the Ticket to an Executor.
//
// The package is pure: it performs no network or model I/O and does not import gosolu/react.
// Every Engine command runs in one Repositories transaction, and external work (launching a
// worker, delivering inputs, relaying notices to the main session, sending replies) is stored
// as an Effect inside that transaction and performed afterwards by an EffectRunner.
package superagent

import (
	"encoding/json"
	"time"
)

// Strength is how strong the basis of a request or a Mandate is.
type Strength string

const (
	// StrengthNone means the Trigger is not admitted to Eval.
	StrengthNone Strength = ""
	// StrengthExplicit is a direct request from an allowed person.
	StrengthExplicit Strength = "explicit"
	// StrengthInferred is a candidate that needs an assessment against the Policy rules.
	StrengthInferred Strength = "inferred"
)

// Addressing records whether a message addresses the agent.
type Addressing string

const (
	AddressNone    Addressing = ""
	AddressMention Addressing = "mention"
	AddressDirect  Addressing = "direct"
)

// Actor is the sender of a Trigger, in its Source's own identities.
type Actor struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Aliases []string `json:"aliases,omitempty"` // other identifiers the allowed list may use (e.g. username)
	Bot     bool     `json:"bot,omitempty"`
}

// Identities returns every identifier of the actor.
func (a Actor) Identities() []string {
	out := make([]string, 0, 1+len(a.Aliases))
	if a.ID != "" {
		out = append(out, a.ID)
	}
	return append(out, a.Aliases...)
}

// Label is a short human-readable actor label.
func (a Actor) Label() string {
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// ChannelAddress is the write side of a Thread: where a reply goes.
type ChannelAddress struct {
	Source  string `json:"source"`
	ChatID  string `json:"chat_id"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// Key identifies the conversation (without the reply target).
func (c ChannelAddress) Key() string { return c.Source + ":" + c.ChatID }

// Trigger is one immutable event from a Source.
type Trigger struct {
	ID        string         `json:"id"`     // unique across Sources, e.g. "telegram:123:456"
	Source    string         `json:"source"` // e.g. "telegram", "feishu"
	Thread    string         `json:"thread"` // primary Thread key
	Related   []string       `json:"related,omitempty"`
	Scope     string         `json:"scope,omitempty"` // receiving scope (admission table row), e.g. a chat id
	Actor     Actor          `json:"actor"`
	Addressed Addressing     `json:"addressed,omitempty"`
	Text      string         `json:"text"`
	Channel   ChannelAddress `json:"channel"`
	At        time.Time      `json:"at"`
}

// Disposition records what happened to a Trigger.
type Disposition string

const (
	DispositionNotAdmitted Disposition = "not_admitted"
	DispositionQueued      Disposition = "queued"
	DispositionEvaluating  Disposition = "evaluating"
	DispositionOwned       Disposition = "owned" // a person's follow-up owned by a Ticket's Executor
	DispositionContext     Disposition = "context"
	DispositionAccepted    Disposition = "accepted"
	DispositionAttached    Disposition = "attached"
	DispositionStopped     Disposition = "stopped"
	DispositionIgnored     Disposition = "ignored"
	DispositionUnavailable Disposition = "unavailable"
)

// HandBack records an Executor handing a follow-up back for routing elsewhere.
type HandBack struct {
	TicketID string `json:"ticket_id"`
	Reason   string `json:"reason"`
}

// TriggerRecord is a stored Trigger plus its gate result and disposition.
type TriggerRecord struct {
	Trigger
	Strength    Strength    `json:"strength,omitempty"`
	Basis       Basis       `json:"basis,omitempty"`
	Disposition Disposition `json:"disposition"`
	DeliveredTo []string    `json:"delivered_to,omitempty"`
	HandedBack  []HandBack  `json:"handed_back,omitempty"`
	Expiries    int         `json:"expiries,omitempty"`
	ReceivedAt  time.Time   `json:"received_at"`
}

// Thread is the ordered set of Triggers that the Source groups together.
type Thread struct {
	Key    string    `json:"key"`
	Queue  []string  `json:"queue,omitempty"`   // Trigger ids waiting for the next Eval
	EvalID string    `json:"eval_id,omitempty"` // the running Eval, at most one
	LastAt time.Time `json:"last_at"`
}

// EvalStatus is the lifecycle of an Eval.
type EvalStatus string

const (
	EvalPending   EvalStatus = "pending"   // candidate relayed, not yet taken by the main session
	EvalDelivered EvalStatus = "delivered" // candidate reached the main session; the deadline runs
	EvalDone      EvalStatus = "done"
)

// Eval is the judgement on a batch of Triggers of one Thread.
type Eval struct {
	ID            string            `json:"id"`
	Thread        string            `json:"thread"`
	TriggerIDs    []string          `json:"trigger_ids"`
	Decided       map[string]string `json:"decided,omitempty"` // trigger id -> decision id or "attach"
	PolicyVersion string            `json:"policy_version"`
	Status        EvalStatus        `json:"status"`
	CreatedAt     time.Time         `json:"created_at"`
	DeliveredAt   *time.Time        `json:"delivered_at,omitempty"`
	Deadline      *time.Time        `json:"deadline,omitempty"`
}

// Undecided returns the Eval's Triggers without a decision.
func (e *Eval) Undecided() []string {
	var out []string
	for _, id := range e.TriggerIDs {
		if _, ok := e.Decided[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// Outcome is the result of an Eval for one Trigger.
type Outcome string

const (
	OutcomeAccept      Outcome = "accept"
	OutcomeStop        Outcome = "stop"
	OutcomeIgnore      Outcome = "ignore"
	OutcomeUnavailable Outcome = "unavailable"
)

// Decision is the recorded result of an Eval (or a direct decision on an explicit request).
type Decision struct {
	ID              string    `json:"id"`
	EvalID          string    `json:"eval_id,omitempty"`
	Thread          string    `json:"thread"`
	TriggerID       string    `json:"trigger_id"`
	Outcome         Outcome   `json:"outcome"`
	Reason          string    `json:"reason,omitempty"`
	TicketID        string    `json:"ticket_id,omitempty"`
	WouldAccept     bool      `json:"would_accept,omitempty"`
	IgnoredFromCard bool      `json:"ignored_from_card,omitempty"`
	MatchedRules    []string  `json:"matched_rules,omitempty"`
	PolicyVersion   string    `json:"policy_version"`
	At              time.Time `json:"at"`
}

// Basis records which part of the Policy admitted a request.
type Basis string

const (
	BasisListed   Basis = "listed"   // an allowed person addressed the agent
	BasisFollowup Basis = "followup" // a person's follow-up on a Thread with Tickets
	BasisTable    Basis = "table"    // an admission table cell
	BasisBot      Basis = "bot"      // a bot on the Source's bot list
)

// Mandate is what an accept grants. It is immutable except for withdrawal and revocation.
type Mandate struct {
	PolicyVersion  string   `json:"policy_version"`
	Scope          string   `json:"scope"`
	Strength       Strength `json:"strength"`
	Basis          Basis    `json:"basis"`
	MatchedRules   []string `json:"matched_rules,omitempty"`
	Requester      Actor    `json:"requester"`
	Source         string   `json:"source"`
	ReceivingScope string   `json:"receiving_scope,omitempty"`
	Addressed      bool     `json:"addressed,omitempty"`
	TriggerID      string   `json:"trigger_id"`
	Withdrawn      bool     `json:"withdrawn,omitempty"` // a person stopped the work
	Revoked        bool     `json:"revoked,omitempty"`   // the Policy no longer holds it
}

// TicketStatus is the lifecycle of a Ticket.
type TicketStatus string

const (
	TicketOpen  TicketStatus = "open"
	TicketEnded TicketStatus = "ended"
)

// Ticket end reasons.
const (
	EndCompleted = "completed"
	EndDropped   = "dropped"
	EndStopped   = "stopped"
	EndRevoked   = "revoked"
)

// Ticket is a unit of authorized work.
type Ticket struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"` // short name, used for the work directory
	Title           string           `json:"title"`
	Note            string           `json:"note,omitempty"` // routing note from the main session
	Status          TicketStatus     `json:"status"`
	EndReason       string           `json:"end_reason,omitempty"`
	Mandate         Mandate          `json:"mandate"`
	DecisionID      string           `json:"decision_id"`
	OriginTrigger   string           `json:"origin_trigger"`
	OriginThread    string           `json:"origin_thread"`
	Channels        []ChannelAddress `json:"channels"`
	AnswerChannel   *ChannelAddress  `json:"answer_channel,omitempty"` // channel of the latest owned human message
	AssignmentID    string           `json:"assignment_id,omitempty"`
	LastReport      *Report          `json:"last_report,omitempty"`
	ReleasedByHuman bool             `json:"released_by_human,omitempty"` // a person wrote after a blocked/failed report
	Attention       string           `json:"attention,omitempty"`
	DeliveryError   string           `json:"delivery_error,omitempty"`
	Acknowledged    bool             `json:"acknowledged,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	EndedAt         *time.Time       `json:"ended_at,omitempty"`
}

// Stopped reports whether a person stopped the Ticket.
func (t *Ticket) Stopped() bool { return t.Mandate.Withdrawn }

// Live reports whether the Ticket is open.
func (t *Ticket) Live() bool { return t.Status == TicketOpen }

// Halted reports whether the latest Report is blocked or failed and no person released it since.
func (t *Ticket) Halted() bool {
	if t.LastReport == nil || t.ReleasedByHuman {
		return false
	}
	return t.LastReport.State == ReportBlocked || t.LastReport.State == ReportFailed
}

// AssignmentStatus is the lifecycle of an Assignment.
type AssignmentStatus string

const (
	AssignmentPending AssignmentStatus = "pending"
	AssignmentRunning AssignmentStatus = "running"
	AssignmentEnded   AssignmentStatus = "ended"
)

// Assignment binds a Ticket to one Executor run.
type Assignment struct {
	ID           string           `json:"id"`
	TicketID     string           `json:"ticket_id"`
	Status       AssignmentStatus `json:"status"`
	Recovery     bool             `json:"recovery,omitempty"` // started after a host restart cut off a turn
	Resume       bool             `json:"resume,omitempty"`   // started because an ended Ticket resumed
	Busy         bool             `json:"busy,omitempty"`     // a turn is running
	TurnReported bool             `json:"turn_reported,omitempty"`
	EndReason    string           `json:"end_reason,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	StartedAt    *time.Time       `json:"started_at,omitempty"`
	EndedAt      *time.Time       `json:"ended_at,omitempty"`
}

// ReportState is the state an Assignment reports to its Ticket.
type ReportState string

const (
	ReportProgress ReportState = "progress"
	ReportWaiting  ReportState = "waiting"
	ReportReady    ReportState = "ready"
	ReportBlocked  ReportState = "blocked"
	ReportFailed   ReportState = "failed"
	ReportDropped  ReportState = "dropped"
)

// Valid reports whether s is a known state.
func (s ReportState) Valid() bool {
	switch s {
	case ReportProgress, ReportWaiting, ReportReady, ReportBlocked, ReportFailed, ReportDropped:
		return true
	}
	return false
}

// Report is a structured status from an Executor.
type Report struct {
	ID           string      `json:"id"`
	TicketID     string      `json:"ticket_id"`
	AssignmentID string      `json:"assignment_id"`
	State        ReportState `json:"state"`
	Summary      string      `json:"summary"`
	Question     string      `json:"question,omitempty"` // an Attention: one concrete question for a person
	At           time.Time   `json:"at"`
}

// SubscriptionKind distinguishes how a Ticket follows a Thread.
type SubscriptionKind string

const (
	SubOrigin   SubscriptionKind = "origin"   // the Thread the Ticket started on
	SubAttached SubscriptionKind = "attached" // attached by the main session
	SubContext  SubscriptionKind = "context"  // followed for context only; lends no authority
)

// Subscription relates a Ticket and a Thread.
type Subscription struct {
	TicketID  string           `json:"ticket_id"`
	Thread    string           `json:"thread"`
	Kind      SubscriptionKind `json:"kind"`
	CreatedAt time.Time        `json:"created_at"`
}

// InputKind is the kind of context delivered to a Ticket.
type InputKind string

const (
	InputTrigger InputKind = "trigger"
	InputText    InputKind = "text"
	InputPolicy  InputKind = "policy"
)

// InputStatus is the delivery lifecycle of an Input.
type InputStatus string

const (
	InputPending   InputStatus = "pending"   // waiting for a running Assignment
	InputQueued    InputStatus = "queued"    // a deliver effect is stored
	InputDelivered InputStatus = "delivered" // reached the Executor Session
	InputRead      InputStatus = "read"      // taken into a turn that ended or reported
	InputHeld      InputStatus = "held"      // recorded but not delivered (bot/CI message to a halted Ticket)
	InputReleased  InputStatus = "released"  // released to Eval when the Ticket ended
	InputHandedOff InputStatus = "handed_back"
)

// Input is context delivered to a Ticket.
type Input struct {
	ID           string      `json:"id"`
	TicketID     string      `json:"ticket_id"`
	Kind         InputKind   `json:"kind"`
	TriggerID    string      `json:"trigger_id,omitempty"`
	Text         string      `json:"text,omitempty"`
	Owned        bool        `json:"owned,omitempty"` // a person's follow-up the Executor decides
	Status       InputStatus `json:"status"`
	AssignmentID string      `json:"assignment_id,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
}

// Undelivered reports whether the Input has not reached an Executor Session.
func (i *Input) Undelivered() bool {
	return i.Status == InputPending || i.Status == InputQueued || i.Status == InputHeld
}

// EffectKind is the kind of stored external work.
type EffectKind string

const (
	EffectRelay   EffectKind = "relay"   // a notice to the main session
	EffectLaunch  EffectKind = "launch"  // start an Assignment
	EffectDeliver EffectKind = "deliver" // deliver Inputs to a running Assignment
	EffectEnd     EffectKind = "end"     // end an Assignment
	EffectReply   EffectKind = "reply"   // send a message through a Channel
)

// EffectStatus is the lifecycle of an Effect.
type EffectStatus string

const (
	EffectPendingStatus EffectStatus = "pending"
	EffectDone          EffectStatus = "done"
	EffectDropped       EffectStatus = "dropped"
)

// Effect is external work stored inside a command's transaction and performed afterwards.
type Effect struct {
	ID        int64           `json:"id"`
	Kind      EffectKind      `json:"kind"`
	Lane      string          `json:"lane,omitempty"` // effects in one lane are performed in order
	Payload   json.RawMessage `json:"payload"`
	Status    EffectStatus    `json:"status"`
	Attempts  int             `json:"attempts"`
	NextAt    time.Time       `json:"next_at"`
	LastError string          `json:"last_error,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// LaunchPayload is the payload of an EffectLaunch.
type LaunchPayload struct {
	TicketID     string `json:"ticket_id"`
	AssignmentID string `json:"assignment_id"`
	Recovery     bool   `json:"recovery,omitempty"`
	Resume       bool   `json:"resume,omitempty"`
	Priority     bool   `json:"priority,omitempty"` // a person's message woke it; may use the reserved slot
}

// DeliverPayload is the payload of an EffectDeliver.
type DeliverPayload struct {
	TicketID     string   `json:"ticket_id"`
	AssignmentID string   `json:"assignment_id"`
	InputIDs     []string `json:"input_ids"`
	Priority     bool     `json:"priority,omitempty"`
}

// EndPayload is the payload of an EffectEnd.
type EndPayload struct {
	TicketID     string `json:"ticket_id"`
	AssignmentID string `json:"assignment_id"`
	Reason       string `json:"reason"`
	Cancel       bool   `json:"cancel,omitempty"` // interrupt a running turn
}

// Reply purposes.
const (
	PurposeAcknowledgement = "acknowledgement"
	PurposeAnswer          = "answer"
	PurposeResult          = "result"
	PurposeAttention       = "attention"
	PurposeNotice          = "notice"
)

// ReplyPayload is the payload of an EffectReply.
type ReplyPayload struct {
	TicketID string         `json:"ticket_id,omitempty"`
	Purpose  string         `json:"purpose"`
	Channel  ChannelAddress `json:"channel"`
	Text     string         `json:"text"`
}

// NoticeKind is the kind of a message relayed to the main session.
type NoticeKind string

const (
	NoticeCandidate       NoticeKind = "candidate"
	NoticeFollowUp        NoticeKind = "follow_up"
	NoticeWorkerReport    NoticeKind = "worker_report"
	NoticeWorkerAttention NoticeKind = "worker_attention"
	NoticeTicketEnded     NoticeKind = "ticket_ended"
	NoticePolicyChanged   NoticeKind = "policy_changed"
)

// Notice is the payload of an EffectRelay: a pre-rendered message for the main session.
type Notice struct {
	Kind     NoticeKind `json:"kind"`
	EvalID   string     `json:"eval_id,omitempty"`
	TicketID string     `json:"ticket_id,omitempty"`
	Text     string     `json:"text"`
	Wake     bool       `json:"wake"` // start a main-session turn; otherwise wait for the next one
}
