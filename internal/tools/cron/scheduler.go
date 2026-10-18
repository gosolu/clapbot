package cron

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react"
	"github.com/robfig/cron/v3"
)

// RunContext keys set by the runtime engine for channel-aware tools.
const (
	ContextKeyChannel   = "clapbot.channel"
	ContextKeyChatID    = "clapbot.chat_id"
	ContextKeyWorkspace = "clapbot.workspace"
)

// TriggerFunc is invoked when a scheduled cron job or one-shot timer fires.
type TriggerFunc func(ctx context.Context, sch store.Schedule)

// Scheduler manages recurring cron jobs and one-shot timers persisted in SQLite.
type Scheduler struct {
	store   *store.SQLiteStore
	trigger TriggerFunc

	mu         sync.Mutex
	cronEngine *cron.Cron
	cronIDs    map[string]cron.EntryID
	timers     map[string]*time.Timer
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewScheduler creates a new Scheduler backed by SQLiteStore.
func NewScheduler(st *store.SQLiteStore, trigger TriggerFunc) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		store:      st,
		trigger:    trigger,
		cronEngine: cron.New(),
		cronIDs:    map[string]cron.EntryID{},
		timers:     map[string]*time.Timer{},
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start loads all enabled schedules from SQLite and starts the cron engine.
func (s *Scheduler) Start(ctx context.Context) error {
	if s.store != nil {
		list, err := s.store.ListSchedules(ctx)
		if err != nil {
			return err
		}
		for _, sch := range list {
			if sch.Enabled {
				_ = s.registerRuntime(sch)
			}
		}
	}
	s.cronEngine.Start()
	return nil
}

// Stop halts all cron jobs and one-shot timers.
func (s *Scheduler) Stop() {
	s.cancel()
	s.cronEngine.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.timers {
		t.Stop()
		delete(s.timers, id)
	}
}

func (s *Scheduler) registerRuntime(sch store.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.unregisterLocked(sch.ID)
	if !sch.Enabled {
		return nil
	}

	if sch.RunAt != nil {
		delay := time.Until(*sch.RunAt)
		if delay <= 0 {
			delay = 500 * time.Millisecond
		}
		schCopy := sch
		s.timers[sch.ID] = time.AfterFunc(delay, func() {
			s.mu.Lock()
			delete(s.timers, schCopy.ID)
			s.mu.Unlock()
			if s.store != nil {
				_ = s.store.MarkScheduleRun(s.ctx, schCopy.ID, true)
			}
			if s.trigger != nil {
				s.trigger(s.ctx, schCopy)
			}
		})
		return nil
	}

	if sch.CronExpr != "" {
		schCopy := sch
		eid, err := s.cronEngine.AddFunc(sch.CronExpr, func() {
			if s.store != nil {
				_ = s.store.MarkScheduleRun(s.ctx, schCopy.ID, false)
			}
			if s.trigger != nil {
				s.trigger(s.ctx, schCopy)
			}
		})
		if err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", sch.CronExpr, err)
		}
		s.cronIDs[sch.ID] = eid
	}
	return nil
}

func (s *Scheduler) unregisterLocked(id string) {
	if eid, ok := s.cronIDs[id]; ok {
		s.cronEngine.Remove(eid)
		delete(s.cronIDs, id)
	}
	if t, ok := s.timers[id]; ok {
		t.Stop()
		delete(s.timers, id)
	}
}

// Add validates, persists, and schedules a new job.
func (s *Scheduler) Add(ctx context.Context, sch store.Schedule) (store.Schedule, error) {
	if sch.ID == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		sch.ID = "sched_" + hex.EncodeToString(b[:])
	}
	if sch.CreatedAt.IsZero() {
		sch.CreatedAt = time.Now().UTC()
	}
	sch.Enabled = true

	if sch.CronExpr != "" {
		if _, err := cron.ParseStandard(sch.CronExpr); err != nil {
			return sch, fmt.Errorf("invalid 5-field cron_expr %q: %w", sch.CronExpr, err)
		}
	} else if sch.RunAt == nil {
		return sch, fmt.Errorf("either cron_expr or delay_seconds must be provided")
	}

	if s.store != nil {
		if err := s.store.SaveSchedule(ctx, sch); err != nil {
			return sch, err
		}
	}
	if err := s.registerRuntime(sch); err != nil {
		return sch, err
	}
	return sch, nil
}

// Remove deletes a schedule by ID from both runtime and SQLite.
func (s *Scheduler) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	s.unregisterLocked(id)
	s.mu.Unlock()
	if s.store != nil {
		return s.store.DeleteSchedule(ctx, id)
	}
	return nil
}

type createScheduleInput struct {
	Name         string `json:"name" desc:"Short descriptive name for the scheduled task or reminder."`
	Prompt       string `json:"prompt" desc:"Instruction or reminder prompt that the agent will execute when the schedule fires."`
	CronExpr     string `json:"cron_expr,omitempty" desc:"Standard 5-field cron expression (e.g. '0 9 * * *' for daily at 9am, '*/15 * * * *' for every 15m). Mutually exclusive with delay_seconds."`
	DelaySeconds int    `json:"delay_seconds,omitempty" desc:"Seconds from now for a one-shot timer (e.g. 600 for 10 minutes). Mutually exclusive with cron_expr."`
	Channel      string `json:"channel,omitempty" desc:"Optional target channel override (defaults to current conversation channel)."`
	ChatID       string `json:"chat_id,omitempty" desc:"Optional target chat ID override (defaults to current conversation chat_id)."`
}

type deleteScheduleInput struct {
	ID string `json:"id" desc:"The schedule ID to delete (e.g. 'sched_a1b2c3d4')."`
}

type emptyInput struct{}

// Tools returns the schedule_create, schedule_list, and schedule_delete tools.
func (s *Scheduler) Tools() []react.Tool {
	createTool := react.NewTool(
		"schedule_create",
		"Create a recurring cron job or a one-shot timer that wakes the agent to execute a prompt and deliver the result to the chat channel.",
		func(ctx context.Context, in createScheduleInput) (string, error) {
			if strings.TrimSpace(in.Prompt) == "" {
				return "", fmt.Errorf("prompt is required")
			}
			channel := strings.TrimSpace(in.Channel)
			chatID := strings.TrimSpace(in.ChatID)
			var sessionID string
			if rc := react.RunContextFrom(ctx); rc != nil {
				sessionID = rc.SessionID
				if channel == "" {
					if v, ok := rc.Get(ContextKeyChannel); ok {
						channel, _ = v.(string)
					}
				}
				if chatID == "" {
					if v, ok := rc.Get(ContextKeyChatID); ok {
						chatID, _ = v.(string)
					}
				}
			}

			sch := store.Schedule{
				Name:      strings.TrimSpace(in.Name),
				Prompt:    strings.TrimSpace(in.Prompt),
				CronExpr:  strings.TrimSpace(in.CronExpr),
				Channel:   channel,
				ChatID:    chatID,
				SessionID: sessionID,
				Enabled:   true,
			}
			if sch.Name == "" {
				sch.Name = "scheduled-task"
			}
			if in.DelaySeconds > 0 {
				t := time.Now().Add(time.Duration(in.DelaySeconds) * time.Second)
				sch.RunAt = &t
			}

			created, err := s.Add(ctx, sch)
			if err != nil {
				return "", err
			}
			if created.RunAt != nil {
				return fmt.Sprintf("Created one-shot timer %s (%q) firing at %s -> %s:%s",
					created.ID, created.Name, created.RunAt.Format(time.RFC3339), created.Channel, created.ChatID), nil
			}
			return fmt.Sprintf("Created recurring cron schedule %s (%q, cron=%q) -> %s:%s",
				created.ID, created.Name, created.CronExpr, created.Channel, created.ChatID), nil
		},
	)

	listTool := react.NewTool(
		"schedule_list",
		"List all configured cron schedules and one-shot timers.",
		func(ctx context.Context, _ emptyInput) (string, error) {
			if s.store == nil {
				return "No schedule store available.", nil
			}
			list, err := s.store.ListSchedules(ctx)
			if err != nil {
				return "", err
			}
			if len(list) == 0 {
				return "No schedules configured.", nil
			}
			var b strings.Builder
			for _, sch := range list {
				status := "enabled"
				if !sch.Enabled {
					status = "completed/disabled"
				}
				spec := sch.CronExpr
				if sch.RunAt != nil {
					spec = "at " + sch.RunAt.Format(time.RFC3339)
				}
				fmt.Fprintf(&b, "- **%s** (%s) [%s] `%s` -> %s:%s | Prompt: %s\n",
					sch.ID, sch.Name, status, spec, sch.Channel, sch.ChatID, sch.Prompt)
			}
			return strings.TrimSpace(b.String()), nil
		},
		react.ReadOnly(),
	)

	deleteTool := react.NewTool(
		"schedule_delete",
		"Delete and cancel a scheduled cron job or timer by ID.",
		func(ctx context.Context, in deleteScheduleInput) (string, error) {
			if strings.TrimSpace(in.ID) == "" {
				return "", fmt.Errorf("id is required")
			}
			if err := s.Remove(ctx, strings.TrimSpace(in.ID)); err != nil {
				return "", err
			}
			return fmt.Sprintf("Deleted schedule %s.", in.ID), nil
		},
	)

	return []react.Tool{createTool, listTool, deleteTool}
}
