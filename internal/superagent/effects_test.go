package superagent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTransactionalOutboxAndRunner(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	fixedNow := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return fixedNow }

	outbox := NewTransactionalOutbox(nowFn)

	var sentReplies []ReplyPayload
	handlers := EffectHandlers{
		Reply: func(ctx context.Context, p ReplyPayload) error {
			sentReplies = append(sentReplies, p)
			return nil
		},
	}
	runner := NewEffectRunner(store, handlers, nowFn)

	// 1. Send via outbox inside transaction
	err := store.Transact(ctx, func(tx Tx) error {
		return outbox.Send(tx, ReplyPayload{
			TicketID: "t_101",
			Purpose:  PurposeAcknowledgement,
			Channel:  ChannelAddress{Source: "telegram", ChatID: "c1"},
			Text:     "Task accepted",
		})
	})
	if err != nil {
		t.Fatalf("outbox.Send failed: %v", err)
	}

	// 2. Run due effects
	n, err := runner.RunDue(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("RunDue: count=%d, err=%v", n, err)
	}
	if len(sentReplies) != 1 || sentReplies[0].Text != "Task accepted" {
		t.Fatalf("unexpected sent replies: %+v", sentReplies)
	}

	// Running again should find 0 due effects
	n2, err := runner.RunDue(ctx, 10)
	if err != nil || n2 != 0 {
		t.Fatalf("expected 0 due effects, got %d", n2)
	}
}

func TestEffectRetryAndBackoff(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	fixedNow := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return fixedNow }

	attempts := 0
	handlers := EffectHandlers{
		Reply: func(ctx context.Context, p ReplyPayload) error {
			attempts++
			return errors.New("network timeout")
		},
	}
	runner := NewEffectRunner(store, handlers, nowFn)
	outbox := NewTransactionalOutbox(nowFn)

	_ = store.Transact(ctx, func(tx Tx) error {
		return outbox.Send(tx, ReplyPayload{
			Purpose: PurposeNotice,
			Channel: ChannelAddress{Source: "feishu", ChatID: "c2"},
			Text:    "Test alert",
		})
	})

	// First execution fails and increments attempts
	n, _ := runner.RunDue(ctx, 10)
	if n != 1 || attempts != 1 {
		t.Fatalf("expected 1 execution attempt, got n=%d attempts=%d", n, attempts)
	}

	// Check updated effect in store
	var eff *Effect
	_ = store.Transact(ctx, func(tx Tx) error {
		effects, _ := tx.GetDueEffects(fixedNow.Add(10*time.Minute), 10)
		for _, e := range effects {
			eff = e
			break
		}
		return nil
	})
	if eff == nil || eff.Attempts != 1 || eff.Status != EffectPendingStatus {
		t.Fatalf("expected effect pending with attempts=1, got: %+v", eff)
	}
	if eff.NextAt.Before(fixedNow.Add(4 * time.Second)) {
		t.Fatalf("expected backoff next_at >= +5s, got %v", eff.NextAt)
	}
}
