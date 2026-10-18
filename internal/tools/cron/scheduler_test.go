package cron

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react"
)

func TestSchedulerAndTools(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var triggered atomic.Int32
	sch := NewScheduler(st, func(ctx context.Context, s store.Schedule) {
		triggered.Add(1)
	})

	ctx := context.Background()
	if err := sch.Start(ctx); err != nil {
		t.Fatalf("sch.Start: %v", err)
	}
	defer sch.Stop()

	// Test tools
	tools := sch.Tools()
	var createTool, listTool, deleteTool react.Tool
	for _, tool := range tools {
		switch tool.Spec().Name {
		case "schedule_create":
			createTool = tool
		case "schedule_list":
			listTool = tool
		case "schedule_delete":
			deleteTool = tool
		}
	}
	if createTool == nil || listTool == nil || deleteTool == nil {
		t.Fatalf("expected all 3 schedule tools")
	}

	rc := &react.RunContext{SessionID: "sess_cron"}
	rc.Set(ContextKeyChannel, "telegram")
	rc.Set(ContextKeyChatID, "chat_99")
	runCtx := react.WithRunContext(ctx, rc)

	// Create a one-shot timer with 1s delay
	createArg, _ := json.Marshal(map[string]any{
		"name":          "reminder",
		"prompt":        "Take a break",
		"delay_seconds": 1,
	})
	res, err := createTool.Call(runCtx, createArg)
	if err != nil || res.IsError {
		t.Fatalf("schedule_create failed: %v, res: %v", err, res)
	}
	if !strings.Contains(res.Text(), "Created one-shot timer") {
		t.Fatalf("unexpected create result: %s", res.Text())
	}

	// List schedules
	res, err = listTool.Call(runCtx, json.RawMessage(`{}`))
	if err != nil || res.IsError || !strings.Contains(res.Text(), "reminder") {
		t.Fatalf("schedule_list failed: %v, res: %v", err, res)
	}

	// Wait for one-shot timer to fire
	time.Sleep(1200 * time.Millisecond)
	if triggered.Load() != 1 {
		t.Fatalf("expected 1 trigger, got %d", triggered.Load())
	}

	// Create a cron schedule
	createCronArg, _ := json.Marshal(map[string]any{
		"name":      "hourly-check",
		"prompt":    "Check system status",
		"cron_expr": "0 * * * *",
	})
	res, err = createTool.Call(runCtx, createCronArg)
	if err != nil || res.IsError || !strings.Contains(res.Text(), "hourly-check") {
		t.Fatalf("schedule_create cron failed: %v, res: %v", err, res)
	}

	// Find the schedule ID from list
	schedules, err := st.ListSchedules(ctx)
	if err != nil || len(schedules) != 2 {
		t.Fatalf("expected 2 schedules in db, got %d", len(schedules))
	}

	var cronID string
	for _, s := range schedules {
		if s.Name == "hourly-check" {
			cronID = s.ID
			break
		}
	}
	if cronID == "" {
		t.Fatalf("missing hourly-check schedule")
	}

	// Delete the cron schedule
	delArg, _ := json.Marshal(map[string]string{"id": cronID})
	res, err = deleteTool.Call(runCtx, delArg)
	if err != nil || res.IsError || !strings.Contains(res.Text(), "Deleted schedule") {
		t.Fatalf("schedule_delete failed: %v, res: %v", err, res)
	}

	schedulesAfter, _ := st.ListSchedules(ctx)
	if len(schedulesAfter) != 1 {
		t.Fatalf("expected 1 schedule after delete, got %d", len(schedulesAfter))
	}
}
