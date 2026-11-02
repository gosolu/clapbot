package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// ErrDropEffect is returned by effect handlers to signal an effect should be dropped permanently.
var ErrDropEffect = errors.New("drop effect permanently")

// EffectHandlers contains the dispatch callbacks for executing stored effects.
type EffectHandlers struct {
	Relay   func(ctx context.Context, n Notice) error
	Launch  func(ctx context.Context, p LaunchPayload) error
	Deliver func(ctx context.Context, p DeliverPayload) error
	End     func(ctx context.Context, p EndPayload) error
	Reply   func(ctx context.Context, p ReplyPayload) error
}

// EffectRunner runs due effects from Repositories with retry and exponential backoff.
type EffectRunner struct {
	repos    Repositories
	handlers EffectHandlers
	now      func() time.Time
}

// NewEffectRunner creates an EffectRunner.
func NewEffectRunner(repos Repositories, handlers EffectHandlers, now func() time.Time) *EffectRunner {
	if now == nil {
		now = time.Now
	}
	return &EffectRunner{
		repos:    repos,
		handlers: handlers,
		now:      now,
	}
}

// CalculateBackoff computes the next retry duration based on the effect kind and attempt count.
// See docs/003 Stored work & Channel failure:
// Relays/Deliveries: 2s doubling up to 60s.
// Replies: 5s doubling up to 300s.
func CalculateBackoff(kind EffectKind, attempts int) time.Duration {
	if attempts <= 0 {
		attempts = 1
	}
	var baseSec, maxSec float64
	switch kind {
	case EffectReply:
		baseSec = 5.0
		maxSec = 300.0
	default:
		baseSec = 2.0
		maxSec = 60.0
	}
	sec := baseSec * math.Pow(2, float64(attempts-1))
	if sec > maxSec {
		sec = maxSec
	}
	return time.Duration(sec * float64(time.Second))
}

// RunDue processes up to limit due effects sequentially or in lane order.
func (r *EffectRunner) RunDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 32
	}

	var due []*Effect
	err := r.repos.Transact(ctx, func(tx Tx) error {
		var err error
		due, err = tx.GetDueEffects(r.now(), limit)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("fetch due effects: %w", err)
	}
	if len(due) == 0 {
		return 0, nil
	}

	processed := 0
	for _, eff := range due {
		if ctx.Err() != nil {
			break
		}
		r.executeOne(ctx, eff)
		processed++
	}
	return processed, nil
}

func (r *EffectRunner) executeOne(ctx context.Context, eff *Effect) {
	var execErr error

	switch eff.Kind {
	case EffectRelay:
		if r.handlers.Relay != nil {
			var n Notice
			if err := json.Unmarshal(eff.Payload, &n); err != nil {
				execErr = ErrDropEffect
			} else {
				execErr = r.handlers.Relay(ctx, n)
			}
		}
	case EffectLaunch:
		if r.handlers.Launch != nil {
			var p LaunchPayload
			if err := json.Unmarshal(eff.Payload, &p); err != nil {
				execErr = ErrDropEffect
			} else {
				execErr = r.handlers.Launch(ctx, p)
			}
		}
	case EffectDeliver:
		if r.handlers.Deliver != nil {
			var p DeliverPayload
			if err := json.Unmarshal(eff.Payload, &p); err != nil {
				execErr = ErrDropEffect
			} else {
				execErr = r.handlers.Deliver(ctx, p)
			}
		}
	case EffectEnd:
		if r.handlers.End != nil {
			var p EndPayload
			if err := json.Unmarshal(eff.Payload, &p); err != nil {
				execErr = ErrDropEffect
			} else {
				execErr = r.handlers.End(ctx, p)
			}
		}
	case EffectReply:
		if r.handlers.Reply != nil {
			var p ReplyPayload
			if err := json.Unmarshal(eff.Payload, &p); err != nil {
				execErr = ErrDropEffect
			} else {
				execErr = r.handlers.Reply(ctx, p)
			}
		}
	default:
		slog.WarnContext(ctx, "unknown effect kind", "kind", eff.Kind, "id", eff.ID)
		execErr = ErrDropEffect
	}

	now := r.now()
	_ = r.repos.Transact(ctx, func(tx Tx) error {
		if execErr == nil {
			eff.Status = EffectDone
			eff.LastError = ""
		} else if errors.Is(execErr, ErrDropEffect) {
			eff.Status = EffectDropped
			eff.LastError = execErr.Error()
		} else {
			eff.Attempts++
			eff.LastError = execErr.Error()
			eff.NextAt = now.Add(CalculateBackoff(eff.Kind, eff.Attempts))
			eff.Status = EffectPendingStatus
		}
		return tx.UpdateEffect(eff)
	})
}
