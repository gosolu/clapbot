package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/clapbot/internal/tools/builtin"
	"github.com/gosolu/react"
	"github.com/gosolu/xslog"
)

// WorkerRunner manages concurrent worker sessions, per-ticket directories, and execution slots.
type WorkerRunner struct {
	eng        *superagent.Engine
	repos      superagent.Repositories
	provider   react.Provider
	model      string
	store      react.Store
	workRoot   string
	prepareCmd string
	maxSlots   int

	mu          sync.Mutex
	activeSlots map[string]context.CancelFunc
}

// Config configures the WorkerRunner.
type Config struct {
	Engine     *superagent.Engine
	Repos      superagent.Repositories
	Provider   react.Provider
	Model      string
	Store      react.Store
	WorkRoot   string
	PrepareCmd string
	MaxSlots   int
}

// NewWorkerRunner creates an active WorkerRunner instance.
func NewWorkerRunner(cfg Config) *WorkerRunner {
	if cfg.MaxSlots <= 0 {
		cfg.MaxSlots = 3
	}
	if cfg.WorkRoot == "" {
		home, _ := os.UserHomeDir()
		cfg.WorkRoot = filepath.Join(home, ".clapbot", "work")
	}
	_ = os.MkdirAll(cfg.WorkRoot, 0755)

	return &WorkerRunner{
		eng:         cfg.Engine,
		repos:       cfg.Repos,
		provider:    cfg.Provider,
		model:       cfg.Model,
		store:       cfg.Store,
		workRoot:    cfg.WorkRoot,
		prepareCmd:  cfg.PrepareCmd,
		maxSlots:    cfg.MaxSlots,
		activeSlots: map[string]context.CancelFunc{},
	}
}

// acquireSlot requests execution capacity. If priority is true (e.g. human wake),
// one extra reserved slot above maxSlots is permitted (docs/003 Concurrency).
func (w *WorkerRunner) acquireSlot(ticketID string, priority bool) (func(), bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	activeCount := len(w.activeSlots)
	capacity := w.maxSlots
	if priority {
		capacity = w.maxSlots + 1
	}

	if activeCount >= capacity {
		return nil, false
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.activeSlots[ticketID] = cancel

	release := func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.activeSlots, ticketID)
		cancel()
	}
	_ = ctx
	return release, true
}

// PrepareTicketDir creates or runs the prepare command for a ticket workspace.
func (w *WorkerRunner) PrepareTicketDir(ctx context.Context, t *superagent.Ticket) (string, error) {
	ticketDir := filepath.Join(w.workRoot, t.Name)
	if fi, err := os.Stat(ticketDir); err == nil && fi.IsDir() {
		return ticketDir, nil
	}

	if strings.TrimSpace(w.prepareCmd) == "" {
		if err := os.MkdirAll(ticketDir, 0755); err != nil {
			return "", fmt.Errorf("create ticket dir: %w", err)
		}
		return ticketDir, nil
	}

	// Run prepare command in workRoot with execution environment variables (docs/003).
	cmd := exec.CommandContext(ctx, "sh", "-c", w.prepareCmd)
	cmd.Dir = w.workRoot
	env := os.Environ()
	env = append(env,
		"CLAPBOT_DIR="+ticketDir,
		"CLAPBOT_NAME="+t.Name,
		"CLAPBOT_TICKET="+t.ID,
		"CLAPBOT_CHANNEL="+t.OriginatingChannel().Source,
		"CLAPBOT_CHAT_ID="+t.OriginatingChannel().ChatID,
	)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("prepare command failed (%w): %s", err, string(out))
	}

	if err := os.MkdirAll(ticketDir, 0755); err != nil {
		return "", fmt.Errorf("ensure ticket dir: %w", err)
	}
	return ticketDir, nil
}

// HandleLaunch processes an EffectLaunch for an assignment.
func (w *WorkerRunner) HandleLaunch(ctx context.Context, p superagent.LaunchPayload) error {
	release, ok := w.acquireSlot(p.TicketID, p.Priority)
	if !ok {
		return fmt.Errorf("no available worker slot (concurrency limit reached)")
	}
	defer release()

	var (
		ticket *superagent.Ticket
		as     *superagent.Assignment
	)
	err := w.repos.Transact(ctx, func(tx superagent.Tx) error {
		var err error
		ticket, err = tx.GetTicket(p.TicketID)
		if err != nil || ticket == nil {
			return fmt.Errorf("ticket %s not found", p.TicketID)
		}
		as, err = tx.GetAssignment(p.AssignmentID)
		return err
	})
	if err != nil {
		return err
	}

	ticketDir, err := w.PrepareTicketDir(ctx, ticket)
	if err != nil {
		slog.ErrorContext(ctx, "failed to prepare ticket dir", "ticket_id", ticket.ID, "error", err)
		_ = w.eng.Report(ctx, ticket.ID, as.ID, superagent.ReportFailed, fmt.Sprintf("Workspace preparation failed: %v", err), "")
		return err
	}

	if err := w.eng.AssignmentStarted(ctx, ticket.ID, as.ID); err != nil {
		return err
	}

	// Fetch delivered inputs for the run prompt.
	var inputs []*superagent.Input
	_ = w.repos.Transact(ctx, func(tx superagent.Tx) error {
		inputs, _ = tx.GetInputsForTicket(ticket.ID)
		return nil
	})

	var promptBuilder strings.Builder
	if p.Recovery {
		promptBuilder.WriteString("[Recovery Note: The system restarted. Please verify your workspace directory and local git status before repeating external actions.]\n\n")
	}
	promptBuilder.WriteString(fmt.Sprintf("You are working on Ticket %s (%s).\n", ticket.Name, ticket.Title))
	if ticket.Note != "" {
		promptBuilder.WriteString(fmt.Sprintf("Routing note: %s\n", ticket.Note))
	}
	for _, in := range inputs {
		if in.Text != "" {
			promptBuilder.WriteString(fmt.Sprintf("\nInput: %s\n", in.Text))
		}
	}

	// Build scoped tools for this ticket session.
	wsTools := append([]react.Tool{}, builtin.NewFilesystem(ticketDir)...)
	wsTools = append(wsTools, builtin.Shell(builtin.WithWorkDir(ticketDir)))
	wsTools = append(wsTools, NewWorkerTools(w.eng)...)

	sessionID := "worker-" + ticket.ID
	runOpts := []react.RunOption{
		react.WithRunTools(wsTools...),
	}

	runCtx := xslog.ContextAppendAttrs(ctx,
		slog.String("session_id", sessionID),
		slog.String("ticket_id", ticket.ID),
	)

	// Ensure run context carries superagent keys.
	hooks := react.Hooks{
		OnRunStart: func(ctx context.Context, rc *react.RunContext) error {
			rc.Set(ContextKeyTicketID, ticket.ID)
			rc.Set(ContextKeyAssignmentID, as.ID)
			rc.Set("clapbot.workspace", ticketDir)
			return nil
		},
		BeforeModel: func(ctx context.Context, rc *react.RunContext, req *react.Request) error {
			var dyn strings.Builder
			dyn.WriteString(fmt.Sprintf("### Ticket Context\n- Ticket ID: `%s`\n- Directory: `%s`\n- Requester: %s\n", ticket.ID, ticketDir, ticket.Mandate.Requester.Label()))
			if len(ticket.Mandate.MatchedRules) > 0 {
				dyn.WriteString(fmt.Sprintf("- Matched Rules: %s\n", strings.Join(ticket.Mandate.MatchedRules, ", ")))
			}
			dyn.WriteString("\nDistinguish completed work from an incomplete draft. Call worker_report when you reach a checkpoint, need a human decision, or finish.")
			if strings.TrimSpace(req.System) != "" {
				req.System = dyn.String() + "\n\n---\n\n" + req.System
			} else {
				req.System = dyn.String()
			}
			return nil
		},
	}

	// Create or run worker turn via agent.
	workerAgent, err := react.New(
		react.WithName("worker"),
		react.WithProvider(w.provider),
		react.WithModel(w.model),
		react.WithStore(w.store),
		react.WithHooks(hooks),
		react.WithMaxTurns(20),
	)
	if err != nil {
		return fmt.Errorf("create worker agent: %w", err)
	}

	_, runErr := workerAgent.Run(runCtx, sessionID, promptBuilder.String(), runOpts...)
	_ = w.eng.TurnEnded(ctx, ticket.ID, as.ID)

	var readIDs []string
	for _, in := range inputs {
		readIDs = append(readIDs, in.ID)
	}
	_ = w.eng.InputsRead(ctx, ticket.ID, readIDs)

	return runErr
}

// HandleDeliver processes an EffectDeliver for inputs.
func (w *WorkerRunner) HandleDeliver(ctx context.Context, p superagent.DeliverPayload) error {
	if err := w.eng.InputsDelivered(ctx, p.TicketID, p.AssignmentID, p.InputIDs); err != nil {
		return err
	}

	// Wake worker session if not currently busy.
	return w.HandleLaunch(ctx, superagent.LaunchPayload{
		TicketID:     p.TicketID,
		AssignmentID: p.AssignmentID,
		Priority:     p.Priority,
	})
}

// HandleEnd cancels an active worker session.
func (w *WorkerRunner) HandleEnd(ctx context.Context, p superagent.EndPayload) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cancel, ok := w.activeSlots[p.TicketID]; ok {
		cancel()
		delete(w.activeSlots, p.TicketID)
	}
	return nil
}
