package channel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosolu/clapbot/internal/tools/cron"
	"github.com/gosolu/react"
)

type mockSender struct {
	sentTexts []string
	sentFiles []string
}

func (m *mockSender) SendText(_ context.Context, ch, cid, text string) error {
	m.sentTexts = append(m.sentTexts, ch+":"+cid+":"+text)
	return nil
}

func (m *mockSender) SendFile(_ context.Context, ch, cid, filePath, caption string) error {
	m.sentFiles = append(m.sentFiles, ch+":"+cid+":"+filePath+":"+caption)
	return nil
}

func TestChannelTools(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "report.txt")
	if err := os.WriteFile(testFile, []byte("summary"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	sender := &mockSender{}
	tools := Tools(sender)

	var sendMsg, sendFile react.Tool
	for _, tool := range tools {
		if tool.Spec().Name == "channel_send_message" {
			sendMsg = tool
		}
		if tool.Spec().Name == "channel_send_file" {
			sendFile = tool
		}
	}

	rc := &react.RunContext{SessionID: "test_session"}
	rc.Set(cron.ContextKeyWorkspace, tmpDir)
	rc.Set(cron.ContextKeyChannel, "telegram")
	rc.Set(cron.ContextKeyChatID, "12345")
	ctx := react.WithRunContext(context.Background(), rc)

	// Test channel_send_message
	res, err := sendMsg.Call(ctx, json.RawMessage(`{"text":"Hello from agent!"}`))
	if err != nil {
		t.Fatalf("sendMsg error: %v", err)
	}
	if !strings.Contains(res.Text(), "Sent message to telegram:12345") {
		t.Fatalf("unexpected result: %s", res.Text())
	}
	if len(sender.sentTexts) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sender.sentTexts))
	}

	// Test channel_send_file inside workspace
	res, err = sendFile.Call(ctx, json.RawMessage(`{"file_path":"report.txt","caption":"My Report"}`))
	if err != nil {
		t.Fatalf("sendFile inside workspace error: %v", err)
	}
	if !strings.Contains(res.Text(), "Sent file report.txt") {
		t.Fatalf("unexpected file result: %s", res.Text())
	}
	if len(sender.sentFiles) != 1 {
		t.Fatalf("expected 1 sent file, got %d", len(sender.sentFiles))
	}

	// Test channel_send_file outside workspace (should be denied)
	callArgs, _ := json.Marshal(map[string]string{"file_path": outsideFile})
	res, err = sendFile.Call(ctx, json.RawMessage(callArgs))
	if err == nil && !res.IsError {
		t.Fatalf("expected error when sending file outside workspace, got nil / non-error: %v", res)
	}
}
