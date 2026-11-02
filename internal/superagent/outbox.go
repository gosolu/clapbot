package superagent

import (
	"encoding/json"
	"fmt"
	"time"
)

// TransactionalOutbox implements Outbox by persisting ReplyPayload into sa_effects
// within the caller's transaction.
type TransactionalOutbox struct {
	now func() time.Time
}

// NewTransactionalOutbox returns an Outbox implementation that stores messages as effects.
func NewTransactionalOutbox(now func() time.Time) *TransactionalOutbox {
	if now == nil {
		now = time.Now
	}
	return &TransactionalOutbox{now: now}
}

// Send implements Outbox. It stores a ReplyPayload effect in the same transaction.
func (o *TransactionalOutbox) Send(tx Tx, msg ReplyPayload) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal reply payload: %w", err)
	}

	lane := "reply:" + msg.Channel.Key()
	if msg.TicketID != "" {
		lane = fmt.Sprintf("reply:%s:%s", msg.TicketID, msg.Channel.Key())
	}

	eff := &Effect{
		Kind:      EffectReply,
		Lane:      lane,
		Payload:   raw,
		Status:    EffectPendingStatus,
		Attempts:  0,
		NextAt:    o.now(),
		CreatedAt: o.now(),
	}
	return tx.EnqueueEffect(eff)
}
