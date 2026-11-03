package superagent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemStore is an in-memory implementation of Repositories for testing and light usage.
type MemStore struct {
	mu            sync.RWMutex
	triggers      map[string]*TriggerRecord
	threads       map[string]*Thread
	evals         map[string]*Eval
	decisions     map[string]*Decision
	tickets       map[string]*Ticket
	assignments   map[string]*Assignment
	subscriptions map[string]*Subscription
	inputs        map[string]*Input
	effects       []*Effect
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		triggers:      map[string]*TriggerRecord{},
		threads:       map[string]*Thread{},
		evals:         map[string]*Eval{},
		decisions:     map[string]*Decision{},
		tickets:       map[string]*Ticket{},
		assignments:   map[string]*Assignment{},
		subscriptions: map[string]*Subscription{},
		inputs:        map[string]*Input{},
		effects:       nil,
	}
}

// Transact runs fn inside a mutex lock.
func (m *MemStore) Transact(ctx context.Context, fn func(tx Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m)
}

func (m *MemStore) GetTrigger(id string) (*TriggerRecord, error) {
	tr, ok := m.triggers[id]
	if !ok {
		return nil, nil
	}
	cp := *tr
	return &cp, nil
}

func (m *MemStore) SaveTrigger(tr *TriggerRecord) error {
	cp := *tr
	m.triggers[tr.ID] = &cp
	return nil
}

func (m *MemStore) GetThread(key string) (*Thread, error) {
	th, ok := m.threads[key]
	if !ok {
		return nil, nil
	}
	cp := *th
	return &cp, nil
}

func (m *MemStore) SaveThread(th *Thread) error {
	cp := *th
	m.threads[th.Key] = &cp
	return nil
}

func (m *MemStore) GetEval(id string) (*Eval, error) {
	ev, ok := m.evals[id]
	if !ok {
		return nil, nil
	}
	cp := *ev
	return &cp, nil
}

func (m *MemStore) SaveEval(ev *Eval) error {
	cp := *ev
	m.evals[ev.ID] = &cp
	return nil
}

func (m *MemStore) FindDueEvals(now time.Time) ([]*Eval, error) {
	var out []*Eval
	for _, ev := range m.evals {
		if ev.Status == EvalDelivered && ev.Deadline != nil && now.After(*ev.Deadline) {
			cp := *ev
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemStore) FindActiveEvals() ([]*Eval, error) {
	var out []*Eval
	for _, ev := range m.evals {
		if ev.Status != EvalDone {
			cp := *ev
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemStore) GetActiveEvalForThread(threadKey string) (*Eval, error) {
	for _, ev := range m.evals {
		if ev.Thread == threadKey && ev.Status != EvalDone {
			cp := *ev
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *MemStore) GetDecision(id string) (*Decision, error) {
	d, ok := m.decisions[id]
	if !ok {
		return nil, nil
	}
	cp := *d
	return &cp, nil
}

func (m *MemStore) SaveDecision(d *Decision) error {
	cp := *d
	m.decisions[d.ID] = &cp
	return nil
}

func (m *MemStore) ListDecisions(outcome Outcome, limit int) ([]*Decision, error) {
	var out []*Decision
	for _, d := range m.decisions {
		if outcome == "" || d.Outcome == outcome {
			cp := *d
			out = append(out, &cp)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemStore) GetLatestDecisionForThread(threadKey string) (*Decision, error) {
	var latest *Decision
	for _, d := range m.decisions {
		if d.Thread == threadKey {
			if latest == nil || d.At.After(latest.At) {
				cp := *d
				latest = &cp
			}
		}
	}
	return latest, nil
}

func (m *MemStore) GetTicket(id string) (*Ticket, error) {
	t, ok := m.tickets[id]
	if !ok {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (m *MemStore) SaveTicket(t *Ticket) error {
	cp := *t
	m.tickets[t.ID] = &cp
	return nil
}

func (m *MemStore) ListTickets(status TicketStatus) ([]*Ticket, error) {
	var out []*Ticket
	for _, t := range m.tickets {
		if status == "" || status == "all" || t.Status == status {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemStore) ListOpenTickets() ([]*Ticket, error) {
	return m.ListTickets(TicketOpen)
}

func (m *MemStore) FindTicketsForThread(threadKey string) ([]*Ticket, error) {
	var out []*Ticket
	for _, sub := range m.subscriptions {
		if sub.Thread == threadKey {
			if t, ok := m.tickets[sub.TicketID]; ok {
				cp := *t
				out = append(out, &cp)
			}
		}
	}
	return out, nil
}

func (m *MemStore) GetAssignment(id string) (*Assignment, error) {
	as, ok := m.assignments[id]
	if !ok {
		return nil, nil
	}
	cp := *as
	return &cp, nil
}

func (m *MemStore) SaveAssignment(a *Assignment) error {
	cp := *a
	m.assignments[a.ID] = &cp
	return nil
}

func (m *MemStore) GetActiveAssignment(ticketID string) (*Assignment, error) {
	for _, a := range m.assignments {
		if a.TicketID == ticketID && a.Status != AssignmentEnded {
			cp := *a
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *MemStore) GetSubscriptionsForThread(threadKey string) ([]*Subscription, error) {
	var out []*Subscription
	for _, s := range m.subscriptions {
		if s.Thread == threadKey {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemStore) GetSubscriptionsForTicket(ticketID string) ([]*Subscription, error) {
	var out []*Subscription
	for _, s := range m.subscriptions {
		if s.TicketID == ticketID {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemStore) SaveSubscription(sub *Subscription) error {
	k := fmt.Sprintf("%s:%s", sub.TicketID, sub.Thread)
	cp := *sub
	m.subscriptions[k] = &cp
	return nil
}

func (m *MemStore) DeleteSubscription(ticketID, threadKey string) error {
	k := fmt.Sprintf("%s:%s", ticketID, threadKey)
	delete(m.subscriptions, k)
	return nil
}

func (m *MemStore) DeleteSubscriptionsForThread(threadKey string) error {
	for k, s := range m.subscriptions {
		if s.Thread == threadKey {
			delete(m.subscriptions, k)
		}
	}
	return nil
}

func (m *MemStore) GetInput(id string) (*Input, error) {
	in, ok := m.inputs[id]
	if !ok {
		return nil, nil
	}
	cp := *in
	return &cp, nil
}

func (m *MemStore) SaveInput(in *Input) error {
	cp := *in
	m.inputs[in.ID] = &cp
	return nil
}

func (m *MemStore) GetInputsForTicket(ticketID string) ([]*Input, error) {
	var out []*Input
	for _, in := range m.inputs {
		if in.TicketID == ticketID {
			cp := *in
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemStore) GetPendingInputs(ticketID string) ([]*Input, error) {
	var out []*Input
	for _, in := range m.inputs {
		if in.TicketID == ticketID && in.Undelivered() {
			cp := *in
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemStore) EnqueueEffect(eff *Effect) error {
	eff.ID = int64(len(m.effects) + 1)
	cp := *eff
	m.effects = append(m.effects, &cp)
	return nil
}

func (m *MemStore) GetDueEffects(now time.Time, limit int) ([]*Effect, error) {
	var out []*Effect
	for _, eff := range m.effects {
		if eff.Status == EffectPendingStatus && !eff.NextAt.After(now) {
			cp := *eff
			out = append(out, &cp)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemStore) UpdateEffect(eff *Effect) error {
	for i, e := range m.effects {
		if e.ID == eff.ID {
			cp := *eff
			m.effects[i] = &cp
			return nil
		}
	}
	return nil
}
