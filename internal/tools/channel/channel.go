package channel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gosolu/clapbot/internal/tools/cron"
	"github.com/gosolu/react"
)

// Sender is implemented by the channel manager / message bus to deliver proactive messages and files.
type Sender interface {
	SendText(ctx context.Context, channel, chatID, text string) error
	SendFile(ctx context.Context, channel, chatID, filePath, caption string) error
}

type sendMessageInput struct {
	Text    string `json:"text" desc:"Message text (Markdown) to send."`
	Channel string `json:"channel,omitempty" desc:"Target channel ('telegram', 'feishu', 'cli'). Defaults to current channel."`
	ChatID  string `json:"chat_id,omitempty" desc:"Target chat ID. Defaults to current chat_id."`
}

type sendFileInput struct {
	FilePath string `json:"file_path" desc:"Path to the file or image to send (relative to workspace or absolute)."`
	Caption  string `json:"caption,omitempty" desc:"Optional caption for the file or image."`
	Channel  string `json:"channel,omitempty" desc:"Target channel ('telegram', 'feishu'). Defaults to current channel."`
	ChatID   string `json:"chat_id,omitempty" desc:"Target chat ID. Defaults to current chat_id."`
}

// Tools returns channel_send_message and channel_send_file tools backed by Sender.
func Tools(sender Sender) []react.Tool {
	sendMsg := react.NewTool(
		"channel_send_message",
		"Proactively send a text message to a chat channel (Telegram, Feishu, or CLI).",
		func(ctx context.Context, in sendMessageInput) (string, error) {
			if sender == nil {
				return "", fmt.Errorf("channel sender not initialized")
			}
			ch, cid := resolveTarget(ctx, in.Channel, in.ChatID)
			if ch == "" {
				return "", fmt.Errorf("channel is required when not running inside a channel context")
			}
			if err := sender.SendText(ctx, ch, cid, in.Text); err != nil {
				slog.ErrorContext(ctx, "send message error", "channel", ch, "chat_id", cid, "error", err)
				return "", err
			}
			return fmt.Sprintf("Sent message to %s:%s", ch, cid), nil
		},
	)

	sendFile := react.NewTool(
		"channel_send_file",
		"Send a local file, document, or image to a chat channel (Telegram or Feishu).",
		func(ctx context.Context, in sendFileInput) (string, error) {
			if sender == nil {
				return "", fmt.Errorf("channel sender not initialized")
			}
			ch, cid := resolveTarget(ctx, in.Channel, in.ChatID)
			if ch == "" {
				return "", fmt.Errorf("channel is required when not running inside a channel context")
			}
			p := strings.TrimSpace(in.FilePath)
			if p == "" {
				return "", fmt.Errorf("file_path is required")
			}
			ws := ""
			if rc := react.RunContextFrom(ctx); rc != nil {
				if w, ok := rc.Get(cron.ContextKeyWorkspace); ok {
					if wsStr, ok := w.(string); ok && wsStr != "" {
						ws = wsStr
					}
				}
			}
			if !filepath.IsAbs(p) {
				if ws != "" {
					p = filepath.Join(ws, p)
				}
			}
			cleanPath := filepath.Clean(p)
			if ws != "" {
				cleanWS := filepath.Clean(ws)
				rel, err := filepath.Rel(cleanWS, cleanPath)
				if err != nil || strings.HasPrefix(rel, "..") {
					return "", fmt.Errorf("access denied: file %q is outside workspace %q", in.FilePath, ws)
				}
			}
			if _, err := os.Stat(cleanPath); err != nil {
				return "", fmt.Errorf("stat file %s: %w", cleanPath, err)
			}
			if err := sender.SendFile(ctx, ch, cid, cleanPath, in.Caption); err != nil {
				slog.ErrorContext(ctx, "send file error", "channel", ch, "chat_id", cid, "file", cleanPath, "error", err)
				return "", err
			}
			return fmt.Sprintf("Sent file %s to %s:%s", filepath.Base(cleanPath), ch, cid), nil
		},
	)

	return []react.Tool{sendMsg, sendFile}
}

func resolveTarget(ctx context.Context, ch, cid string) (string, string) {
	ch = strings.TrimSpace(ch)
	cid = strings.TrimSpace(cid)
	if rc := react.RunContextFrom(ctx); rc != nil {
		if ch == "" {
			if v, ok := rc.Get(cron.ContextKeyChannel); ok {
				ch, _ = v.(string)
			}
		}
		if cid == "" {
			if v, ok := rc.Get(cron.ContextKeyChatID); ok {
				cid, _ = v.(string)
			}
		}
	}
	return ch, cid
}
