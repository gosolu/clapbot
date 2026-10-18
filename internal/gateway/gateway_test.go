package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react"
	"github.com/gosolu/react/provider/mock"
)

func TestGatewayServerAndClientStream(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	router, err := provider.NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock-1"}, mock.New(
		mock.Text("Streaming over WebSocket works!"),
	))

	eng, err := runtime.NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	srv := NewServer("127.0.0.1:0", eng)
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	cli := NewClient(srv.Addr())
	if !cli.Ping(ctx) {
		t.Fatalf("expected Ping() == true")
	}

	var gotText strings.Builder
	for frame, err := range cli.StreamTurn(ctx, runtime.TurnRequest{
		SessionID: "ws_s1",
		Channel:   "cli",
		Input:     "Hello WS",
	}) {
		if err != nil {
			t.Fatalf("StreamTurn err: %v", err)
		}
		if frame.EventType == react.EventTextDelta {
			gotText.WriteString(frame.Text)
		}
	}
	if gotText.String() != "Streaming over WebSocket works!" {
		t.Fatalf("expected streamed text, got %q", gotText.String())
	}

	cmdRes, err := cli.Command(ctx, runtime.TurnRequest{
		SessionID: "ws_s1",
		Channel:   "cli",
		Input:     "/status",
	})
	if err != nil || !cmdRes.Handled || !strings.Contains(cmdRes.Output, "ws_s1") {
		t.Fatalf("unexpected Command(/status): %+v, err=%v", cmdRes, err)
	}
}

func TestGatewayTokenAuth(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.GatewayToken = "secret-token-123"

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	router, err := provider.NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock-1"}, mock.New(
		mock.Text("Auth test response"),
	))

	eng, err := runtime.NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	srv := NewServer("127.0.0.1:0", eng)
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	// Client without token should be denied for command
	badCli := NewClient(srv.Addr())
	_, err = badCli.Command(ctx, runtime.TurnRequest{SessionID: "s1", Input: "/status"})
	if err == nil {
		t.Fatalf("expected error for unauthenticated client, got nil")
	}

	// Client with matching token should succeed
	goodCli := NewClient(srv.Addr(), "secret-token-123")
	cmdRes, err := goodCli.Command(ctx, runtime.TurnRequest{SessionID: "s1", Input: "/status"})
	if err != nil || !cmdRes.Handled {
		t.Fatalf("expected authenticated command success, got res=%+v, err=%v", cmdRes, err)
	}
}
