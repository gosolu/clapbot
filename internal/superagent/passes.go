package superagent

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Passes coordinates periodic background maintenance: processing due effects and checking eval timeouts.
type Passes struct {
	runner *EffectRunner
	eng    *Engine

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewPasses creates a Passes coordinator.
func NewPasses(runner *EffectRunner, eng *Engine) *Passes {
	return &Passes{
		runner: runner,
		eng:    eng,
	}
}

// Tick executes one maintenance pass: due effects followed by eval timeout checks.
func (p *Passes) Tick(ctx context.Context) error {
	if p.runner != nil {
		if _, err := p.runner.RunDue(ctx, 32); err != nil {
			slog.ErrorContext(ctx, "passes run due effects error", "error", err)
		}
	}
	if p.eng != nil {
		if err := p.eng.EvaluateDue(ctx); err != nil {
			slog.ErrorContext(ctx, "passes evaluate due error", "error", err)
		}
	}
	return nil
}

// Start begins a background ticker running Tick every interval (default 2s).
func (p *Passes) Start(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	loopCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				_ = p.Tick(loopCtx)
			}
		}
	}()

	return nil
}

// Stop terminates the background ticker.
func (p *Passes) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
}
