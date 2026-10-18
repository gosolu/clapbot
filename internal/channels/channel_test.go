package channels

import (
	"context"
	"strings"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/react/provider/mock"
)

type mockTransport struct {
	initials []string
	edits    []string
	chunks   []string
	typings  int
}

func (m *mockTransport) SendInitial(_ context.Context, _, _, text string) (string, error) {
	m.initials = append(m.initials, text)
	return "msg_1", nil
}

func (m *mockTransport) EditMessage(_ context.Context, _, _, text string) error {
	m.edits = append(m.edits, text)
	return nil
}

func (m *mockTransport) SendChunk(_ context.Context, _, text string) error {
	m.chunks = append(m.chunks, text)
	return nil
}

func (m *mockTransport) SendTyping(_ context.Context, _ string) {
	m.typings++
}

func TestExecuteChannelTurnLiveEditAndChunk(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg, _ := config.Load(tmp)
	st, _ := store.Open(":memory:")
	defer func() { _ = st.Close() }()

	router, _ := provider.NewRouter(cfg)
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock-1"}, mock.New(
		mock.ToolCalls("", mock.Call("list_dir", map[string]any{"path": "."})),
		mock.Text("Directory checked successfully!"),
	))

	eng, err := runtime.NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	mt := &mockTransport{}
	err = ExecuteChannelTurn(ctx, eng, mt, runtime.TurnRequest{
		SessionID: "tg_1",
		Channel:   "telegram",
		ChatID:    "100",
		Input:     "Check files",
	}, "42", "edit")
	if err != nil {
		t.Fatalf("ExecuteChannelTurn: %v", err)
	}
	if len(mt.initials) == 0 {
		t.Fatalf("expected initial message")
	}
	if len(mt.edits) == 0 || mt.edits[len(mt.edits)-1] != "Directory checked successfully!" {
		t.Fatalf("expected final edit with output, got edits=%v", mt.edits)
	}

	chunks := ChunkText(strings.Repeat("a", 4000)+"\n"+strings.Repeat("b", 1000), 3800)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
}
