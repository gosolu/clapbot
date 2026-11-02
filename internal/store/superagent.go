package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gosolu/clapbot/internal/superagent"
)

var _ superagent.Repositories = (*SQLiteStore)(nil)

// Transact implements superagent.Repositories.
func (s *SQLiteStore) Transact(ctx context.Context, fn func(tx superagent.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stx := &superagentTx{tx: tx}
	if err := fn(stx); err != nil {
		return err
	}
	return tx.Commit()
}

type superagentTx struct {
	tx *sql.Tx
}

// =============================================================================
// Triggers
// =============================================================================

func (s *superagentTx) GetTrigger(id string) (*superagent.TriggerRecord, error) {
	row := s.tx.QueryRow(`
		SELECT id, source, thread, scope, actor_json, addressed, text_content, channel_json,
		       strength, basis, disposition, delivered_to, handed_back_json, expiries, at, received_at
		FROM sa_triggers WHERE id = ?`, id)

	var (
		tr             superagent.TriggerRecord
		actorJSON      string
		channelJSON    string
		deliveredJSON  string
		handedBackJSON string
	)

	err := row.Scan(
		&tr.ID, &tr.Source, &tr.Thread, &tr.Scope, &actorJSON, &tr.Addressed, &tr.Text, &channelJSON,
		&tr.Strength, &tr.Basis, &tr.Disposition, &deliveredJSON, &handedBackJSON, &tr.Expiries, &tr.At, &tr.ReceivedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(actorJSON), &tr.Actor)
	_ = json.Unmarshal([]byte(channelJSON), &tr.Channel)
	_ = json.Unmarshal([]byte(deliveredJSON), &tr.DeliveredTo)
	_ = json.Unmarshal([]byte(handedBackJSON), &tr.HandedBack)
	return &tr, nil
}

func (s *superagentTx) SaveTrigger(tr *superagent.TriggerRecord) error {
	actorJSON, _ := json.Marshal(tr.Actor)
	channelJSON, _ := json.Marshal(tr.Channel)
	deliveredJSON, _ := json.Marshal(tr.DeliveredTo)
	handedBackJSON, _ := json.Marshal(tr.HandedBack)

	_, err := s.tx.Exec(`
		INSERT INTO sa_triggers (
			id, source, thread, scope, actor_json, addressed, text_content, channel_json,
			strength, basis, disposition, delivered_to, handed_back_json, expiries, at, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			disposition = excluded.disposition,
			delivered_to = excluded.delivered_to,
			handed_back_json = excluded.handed_back_json,
			expiries = excluded.expiries,
			strength = excluded.strength,
			basis = excluded.basis`,
		tr.ID, tr.Source, tr.Thread, tr.Scope, string(actorJSON), string(tr.Addressed), tr.Text, string(channelJSON),
		string(tr.Strength), string(tr.Basis), string(tr.Disposition), string(deliveredJSON), string(handedBackJSON),
		tr.Expiries, tr.At, tr.ReceivedAt,
	)
	return err
}

// =============================================================================
// Threads
// =============================================================================

func (s *superagentTx) GetThread(key string) (*superagent.Thread, error) {
	row := s.tx.QueryRow(`SELECT key, queue_json, eval_id, last_at FROM sa_threads WHERE key = ?`, key)
	var (
		th        superagent.Thread
		queueJSON string
	)
	err := row.Scan(&th.Key, &queueJSON, &th.EvalID, &th.LastAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(queueJSON), &th.Queue)
	return &th, nil
}

func (s *superagentTx) SaveThread(th *superagent.Thread) error {
	queueJSON, _ := json.Marshal(th.Queue)
	_, err := s.tx.Exec(`
		INSERT INTO sa_threads (key, queue_json, eval_id, last_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			queue_json = excluded.queue_json,
			eval_id = excluded.eval_id,
			last_at = excluded.last_at`,
		th.Key, string(queueJSON), th.EvalID, th.LastAt,
	)
	return err
}

// =============================================================================
// Evals
// =============================================================================

func (s *superagentTx) GetEval(id string) (*superagent.Eval, error) {
	row := s.tx.QueryRow(`
		SELECT id, thread, trigger_ids_json, decided_json, policy_version, status, created_at, delivered_at, deadline
		FROM sa_evals WHERE id = ?`, id)

	var (
		ev          superagent.Eval
		triggersRaw string
		decidedRaw  string
	)
	err := row.Scan(
		&ev.ID, &ev.Thread, &triggersRaw, &decidedRaw, &ev.PolicyVersion, &ev.Status, &ev.CreatedAt, &ev.DeliveredAt, &ev.Deadline,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(triggersRaw), &ev.TriggerIDs)
	_ = json.Unmarshal([]byte(decidedRaw), &ev.Decided)
	return &ev, nil
}

func (s *superagentTx) SaveEval(ev *superagent.Eval) error {
	triggersRaw, _ := json.Marshal(ev.TriggerIDs)
	decidedRaw, _ := json.Marshal(ev.Decided)

	_, err := s.tx.Exec(`
		INSERT INTO sa_evals (id, thread, trigger_ids_json, decided_json, policy_version, status, created_at, delivered_at, deadline)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			decided_json = excluded.decided_json,
			status = excluded.status,
			delivered_at = excluded.delivered_at,
			deadline = excluded.deadline`,
		ev.ID, ev.Thread, string(triggersRaw), string(decidedRaw), ev.PolicyVersion, string(ev.Status), ev.CreatedAt, ev.DeliveredAt, ev.Deadline,
	)
	return err
}

func (s *superagentTx) FindDueEvals(now time.Time) ([]*superagent.Eval, error) {
	rows, err := s.tx.Query(`
		SELECT id, thread, trigger_ids_json, decided_json, policy_version, status, created_at, delivered_at, deadline
		FROM sa_evals
		WHERE status = 'delivered' AND deadline IS NOT NULL AND deadline <= ?`, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*superagent.Eval
	for rows.Next() {
		var (
			ev          superagent.Eval
			triggersRaw string
			decidedRaw  string
		)
		if err := rows.Scan(
			&ev.ID, &ev.Thread, &triggersRaw, &decidedRaw, &ev.PolicyVersion, &ev.Status, &ev.CreatedAt, &ev.DeliveredAt, &ev.Deadline,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(triggersRaw), &ev.TriggerIDs)
		_ = json.Unmarshal([]byte(decidedRaw), &ev.Decided)
		out = append(out, &ev)
	}
	return out, rows.Err()
}

func (s *superagentTx) GetActiveEvalForThread(threadKey string) (*superagent.Eval, error) {
	row := s.tx.QueryRow(`
		SELECT id, thread, trigger_ids_json, decided_json, policy_version, status, created_at, delivered_at, deadline
		FROM sa_evals
		WHERE thread = ? AND status != 'done' LIMIT 1`, threadKey)
	var (
		ev          superagent.Eval
		triggersRaw string
		decidedRaw  string
	)
	err := row.Scan(
		&ev.ID, &ev.Thread, &triggersRaw, &decidedRaw, &ev.PolicyVersion, &ev.Status, &ev.CreatedAt, &ev.DeliveredAt, &ev.Deadline,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(triggersRaw), &ev.TriggerIDs)
	_ = json.Unmarshal([]byte(decidedRaw), &ev.Decided)
	return &ev, nil
}

// =============================================================================
// Decisions
// =============================================================================

func (s *superagentTx) GetDecision(id string) (*superagent.Decision, error) {
	row := s.tx.QueryRow(`
		SELECT id, eval_id, thread, trigger_id, outcome, reason, ticket_id, would_accept, matched_rules_json, policy_version, at
		FROM sa_decisions WHERE id = ?`, id)

	var (
		d        superagent.Decision
		rulesRaw string
	)
	err := row.Scan(
		&d.ID, &d.EvalID, &d.Thread, &d.TriggerID, &d.Outcome, &d.Reason, &d.TicketID, &d.WouldAccept, &rulesRaw, &d.PolicyVersion, &d.At,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(rulesRaw), &d.MatchedRules)
	return &d, nil
}

func (s *superagentTx) SaveDecision(d *superagent.Decision) error {
	rulesRaw, _ := json.Marshal(d.MatchedRules)
	_, err := s.tx.Exec(`
		INSERT INTO sa_decisions (id, eval_id, thread, trigger_id, outcome, reason, ticket_id, would_accept, matched_rules_json, policy_version, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			reason = excluded.reason`,
		d.ID, d.EvalID, d.Thread, d.TriggerID, string(d.Outcome), d.Reason, d.TicketID, d.WouldAccept, string(rulesRaw), d.PolicyVersion, d.At,
	)
	return err
}

func (s *superagentTx) GetLatestDecisionForThread(threadKey string) (*superagent.Decision, error) {
	row := s.tx.QueryRow(`
		SELECT id, eval_id, thread, trigger_id, outcome, reason, ticket_id, would_accept, matched_rules_json, policy_version, at
		FROM sa_decisions WHERE thread = ? ORDER BY at DESC LIMIT 1`, threadKey)

	var (
		d        superagent.Decision
		rulesRaw string
	)
	err := row.Scan(
		&d.ID, &d.EvalID, &d.Thread, &d.TriggerID, &d.Outcome, &d.Reason, &d.TicketID, &d.WouldAccept, &rulesRaw, &d.PolicyVersion, &d.At,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(rulesRaw), &d.MatchedRules)
	return &d, nil
}

// =============================================================================
// Tickets
// =============================================================================

func (s *superagentTx) GetTicket(id string) (*superagent.Ticket, error) {
	row := s.tx.QueryRow(`
		SELECT id, name, title, note, status, end_reason, mandate_json, decision_id,
		       origin_trigger, origin_thread, channels_json, answer_channel_json,
		       assignment_id, last_report_json, released_by_human, attention, delivery_error, acknowledged,
		       created_at, updated_at, ended_at
		FROM sa_tickets WHERE id = ?`, id)

	var (
		t            superagent.Ticket
		mandateRaw   string
		channelsRaw  string
		answerRaw    sql.NullString
		reportRaw    sql.NullString
	)

	err := row.Scan(
		&t.ID, &t.Name, &t.Title, &t.Note, &t.Status, &t.EndReason, &mandateRaw, &t.DecisionID,
		&t.OriginTrigger, &t.OriginThread, &channelsRaw, &answerRaw,
		&t.AssignmentID, &reportRaw, &t.ReleasedByHuman, &t.Attention, &t.DeliveryError, &t.Acknowledged,
		&t.CreatedAt, &t.UpdatedAt, &t.EndedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(mandateRaw), &t.Mandate)
	_ = json.Unmarshal([]byte(channelsRaw), &t.Channels)
	if answerRaw.Valid && answerRaw.String != "" {
		var ac superagent.ChannelAddress
		if err := json.Unmarshal([]byte(answerRaw.String), &ac); err == nil {
			t.AnswerChannel = &ac
		}
	}
	if reportRaw.Valid && reportRaw.String != "" {
		var rep superagent.Report
		if err := json.Unmarshal([]byte(reportRaw.String), &rep); err == nil {
			t.LastReport = &rep
		}
	}
	return &t, nil
}

func (s *superagentTx) SaveTicket(t *superagent.Ticket) error {
	mandateRaw, _ := json.Marshal(t.Mandate)
	channelsRaw, _ := json.Marshal(t.Channels)
	var answerRaw, reportRaw any
	if t.AnswerChannel != nil {
		b, _ := json.Marshal(t.AnswerChannel)
		answerRaw = string(b)
	}
	if t.LastReport != nil {
		b, _ := json.Marshal(t.LastReport)
		reportRaw = string(b)
	}

	_, err := s.tx.Exec(`
		INSERT INTO sa_tickets (
			id, name, title, note, status, end_reason, mandate_json, decision_id,
			origin_trigger, origin_thread, channels_json, answer_channel_json,
			assignment_id, last_report_json, released_by_human, attention, delivery_error, acknowledged,
			created_at, updated_at, ended_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			end_reason = excluded.end_reason,
			mandate_json = excluded.mandate_json,
			assignment_id = excluded.assignment_id,
			answer_channel_json = excluded.answer_channel_json,
			last_report_json = excluded.last_report_json,
			released_by_human = excluded.released_by_human,
			attention = excluded.attention,
			delivery_error = excluded.delivery_error,
			acknowledged = excluded.acknowledged,
			updated_at = excluded.updated_at,
			ended_at = excluded.ended_at`,
		t.ID, t.Name, t.Title, t.Note, string(t.Status), t.EndReason, string(mandateRaw), t.DecisionID,
		t.OriginTrigger, t.OriginThread, string(channelsRaw), answerRaw,
		t.AssignmentID, reportRaw, t.ReleasedByHuman, t.Attention, t.DeliveryError, t.Acknowledged,
		t.CreatedAt, t.UpdatedAt, t.EndedAt,
	)
	return err
}

func (s *superagentTx) ListOpenTickets() ([]*superagent.Ticket, error) {
	rows, err := s.tx.Query(`SELECT id FROM sa_tickets WHERE status = 'open'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	var out []*superagent.Ticket
	for _, id := range ids {
		if t, _ := s.GetTicket(id); t != nil {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *superagentTx) FindTicketsForThread(threadKey string) ([]*superagent.Ticket, error) {
	rows, err := s.tx.Query(`SELECT ticket_id FROM sa_subscriptions WHERE thread = ?`, threadKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	var out []*superagent.Ticket
	for _, id := range ids {
		if t, _ := s.GetTicket(id); t != nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// =============================================================================
// Assignments
// =============================================================================

func (s *superagentTx) GetAssignment(id string) (*superagent.Assignment, error) {
	row := s.tx.QueryRow(`
		SELECT id, ticket_id, status, recovery, resume, busy, turn_reported, end_reason, created_at, started_at, ended_at
		FROM sa_assignments WHERE id = ?`, id)

	var a superagent.Assignment
	err := row.Scan(
		&a.ID, &a.TicketID, &a.Status, &a.Recovery, &a.Resume, &a.Busy, &a.TurnReported, &a.EndReason,
		&a.CreatedAt, &a.StartedAt, &a.EndedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *superagentTx) SaveAssignment(a *superagent.Assignment) error {
	_, err := s.tx.Exec(`
		INSERT INTO sa_assignments (id, ticket_id, status, recovery, resume, busy, turn_reported, end_reason, created_at, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			busy = excluded.busy,
			turn_reported = excluded.turn_reported,
			end_reason = excluded.end_reason,
			started_at = excluded.started_at,
			ended_at = excluded.ended_at`,
		a.ID, a.TicketID, string(a.Status), a.Recovery, a.Resume, a.Busy, a.TurnReported, a.EndReason,
		a.CreatedAt, a.StartedAt, a.EndedAt,
	)
	return err
}

func (s *superagentTx) GetActiveAssignment(ticketID string) (*superagent.Assignment, error) {
	row := s.tx.QueryRow(`
		SELECT id, ticket_id, status, recovery, resume, busy, turn_reported, end_reason, created_at, started_at, ended_at
		FROM sa_assignments WHERE ticket_id = ? AND status != 'ended' LIMIT 1`, ticketID)

	var a superagent.Assignment
	err := row.Scan(
		&a.ID, &a.TicketID, &a.Status, &a.Recovery, &a.Resume, &a.Busy, &a.TurnReported, &a.EndReason,
		&a.CreatedAt, &a.StartedAt, &a.EndedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// =============================================================================
// Subscriptions
// =============================================================================

func (s *superagentTx) GetSubscriptionsForThread(threadKey string) ([]*superagent.Subscription, error) {
	rows, err := s.tx.Query(`SELECT ticket_id, thread, kind, created_at FROM sa_subscriptions WHERE thread = ?`, threadKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*superagent.Subscription
	for rows.Next() {
		var sub superagent.Subscription
		if err := rows.Scan(&sub.TicketID, &sub.Thread, &sub.Kind, &sub.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &sub)
	}
	return out, rows.Err()
}

func (s *superagentTx) GetSubscriptionsForTicket(ticketID string) ([]*superagent.Subscription, error) {
	rows, err := s.tx.Query(`SELECT ticket_id, thread, kind, created_at FROM sa_subscriptions WHERE ticket_id = ?`, ticketID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*superagent.Subscription
	for rows.Next() {
		var sub superagent.Subscription
		if err := rows.Scan(&sub.TicketID, &sub.Thread, &sub.Kind, &sub.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &sub)
	}
	return out, rows.Err()
}

func (s *superagentTx) SaveSubscription(sub *superagent.Subscription) error {
	_, err := s.tx.Exec(`
		INSERT INTO sa_subscriptions (ticket_id, thread, kind, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(ticket_id, thread) DO UPDATE SET kind = excluded.kind`,
		sub.TicketID, sub.Thread, string(sub.Kind), sub.CreatedAt,
	)
	return err
}

func (s *superagentTx) DeleteSubscription(ticketID, threadKey string) error {
	_, err := s.tx.Exec(`DELETE FROM sa_subscriptions WHERE ticket_id = ? AND thread = ?`, ticketID, threadKey)
	return err
}

func (s *superagentTx) DeleteSubscriptionsForThread(threadKey string) error {
	_, err := s.tx.Exec(`DELETE FROM sa_subscriptions WHERE thread = ?`, threadKey)
	return err
}

// =============================================================================
// Inputs
// =============================================================================

func (s *superagentTx) GetInput(id string) (*superagent.Input, error) {
	row := s.tx.QueryRow(`
		SELECT id, ticket_id, kind, trigger_id, text_content, owned, status, assignment_id, created_at
		FROM sa_inputs WHERE id = ?`, id)

	var in superagent.Input
	err := row.Scan(
		&in.ID, &in.TicketID, &in.Kind, &in.TriggerID, &in.Text, &in.Owned, &in.Status, &in.AssignmentID, &in.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

func (s *superagentTx) SaveInput(in *superagent.Input) error {
	_, err := s.tx.Exec(`
		INSERT INTO sa_inputs (id, ticket_id, kind, trigger_id, text_content, owned, status, assignment_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			assignment_id = excluded.assignment_id`,
		in.ID, in.TicketID, string(in.Kind), in.TriggerID, in.Text, in.Owned, string(in.Status), in.AssignmentID, in.CreatedAt,
	)
	return err
}

func (s *superagentTx) GetInputsForTicket(ticketID string) ([]*superagent.Input, error) {
	rows, err := s.tx.Query(`
		SELECT id, ticket_id, kind, trigger_id, text_content, owned, status, assignment_id, created_at
		FROM sa_inputs WHERE ticket_id = ? ORDER BY created_at ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*superagent.Input
	for rows.Next() {
		var in superagent.Input
		if err := rows.Scan(
			&in.ID, &in.TicketID, &in.Kind, &in.TriggerID, &in.Text, &in.Owned, &in.Status, &in.AssignmentID, &in.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, &in)
	}
	return out, rows.Err()
}

func (s *superagentTx) GetPendingInputs(ticketID string) ([]*superagent.Input, error) {
	rows, err := s.tx.Query(`
		SELECT id, ticket_id, kind, trigger_id, text_content, owned, status, assignment_id, created_at
		FROM sa_inputs
		WHERE ticket_id = ? AND status IN ('pending', 'queued') ORDER BY created_at ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*superagent.Input
	for rows.Next() {
		var in superagent.Input
		if err := rows.Scan(
			&in.ID, &in.TicketID, &in.Kind, &in.TriggerID, &in.Text, &in.Owned, &in.Status, &in.AssignmentID, &in.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, &in)
	}
	return out, rows.Err()
}

// =============================================================================
// Effects
// =============================================================================

func (s *superagentTx) EnqueueEffect(eff *superagent.Effect) error {
	res, err := s.tx.Exec(`
		INSERT INTO sa_effects (kind, lane, payload, status, attempts, next_at, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(eff.Kind), eff.Lane, eff.Payload, string(eff.Status), eff.Attempts, eff.NextAt, eff.LastError, eff.CreatedAt,
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err == nil {
		eff.ID = id
	}
	return nil
}

func (s *superagentTx) GetDueEffects(now time.Time, limit int) ([]*superagent.Effect, error) {
	query := `
		SELECT id, kind, lane, payload, status, attempts, next_at, last_error, created_at
		FROM sa_effects
		WHERE status = 'pending' AND next_at <= ? ORDER BY id ASC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.tx.Query(query, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*superagent.Effect
	for rows.Next() {
		var eff superagent.Effect
		if err := rows.Scan(
			&eff.ID, &eff.Kind, &eff.Lane, &eff.Payload, &eff.Status, &eff.Attempts, &eff.NextAt, &eff.LastError, &eff.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, &eff)
	}
	return out, rows.Err()
}

func (s *superagentTx) UpdateEffect(eff *superagent.Effect) error {
	_, err := s.tx.Exec(`
		UPDATE sa_effects
		SET status = ?, attempts = ?, next_at = ?, last_error = ?
		WHERE id = ?`,
		string(eff.Status), eff.Attempts, eff.NextAt, eff.LastError, eff.ID,
	)
	return err
}
