package channels

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/react"
)

// MessageTransport is implemented by Telegram and Feishu channel adapters to support
// debounced live message editing and final delivery.
type MessageTransport interface {
	SendInitial(ctx context.Context, chatID, replyToMsgID, text string) (messageID string, err error)
	EditMessage(ctx context.Context, chatID, messageID, text string) error
	SendChunk(ctx context.Context, chatID, text string) error
	SendTyping(ctx context.Context, chatID string)
}

// ExecuteChannelTurn runs a turn on Engine for an inbound chat-app message, handling slash commands,
// rate-limited live message editing (or final-only mode), and long-message chunking.
func ExecuteChannelTurn(
	ctx context.Context,
	eng *runtime.Engine,
	transport MessageTransport,
	req runtime.TurnRequest,
	replyToMsgID string,
	streamMode string,
) error {
	// 1. Check slash commands first.
	if strings.HasPrefix(strings.TrimSpace(req.Input), "/") && len(req.Parts) == 0 {
		cmdRes, err := eng.HandleCommand(ctx, req)
		if err != nil {
			_, _ = transport.SendInitial(ctx, req.ChatID, replyToMsgID, "❌ Error: "+err.Error())
			return err
		}
		if cmdRes.Handled {
			_, err := transport.SendInitial(ctx, req.ChatID, replyToMsgID, cmdRes.Output)
			return err
		}
	}

	transport.SendTyping(ctx, req.ChatID)
	editMode := !strings.EqualFold(streamMode, "final")

	var (
		liveMsgID string
		lastEdit  time.Time
		toolSteps []string
		textBuf   strings.Builder
		finalText string
	)

	renderLive := func(done bool) string {
		var b strings.Builder
		if len(toolSteps) > 0 && !done {
			for _, st := range toolSteps {
				b.WriteString(st)
				b.WriteByte('\n')
			}
			b.WriteByte('\n')
		}
		cur := strings.TrimSpace(textBuf.String())
		if cur != "" {
			b.WriteString(cur)
		} else if !done {
			if len(toolSteps) == 0 {
				b.WriteString("💭 Thinking...")
			}
		}
		out := strings.TrimSpace(b.String())
		if out == "" {
			out = "💭 ..."
		}
		return truncateRunes(out, 3600)
	}

	flushLive := func(force bool) {
		if !editMode {
			return
		}
		if !force && time.Since(lastEdit) < 1100*time.Millisecond {
			return
		}
		content := renderLive(false)
		if liveMsgID == "" {
			mid, err := transport.SendInitial(ctx, req.ChatID, replyToMsgID, content)
			if err == nil {
				liveMsgID = mid
				lastEdit = time.Now()
			}
			return
		}
		_ = transport.EditMessage(ctx, req.ChatID, liveMsgID, content)
		lastEdit = time.Now()
	}

	for ev, err := range eng.StreamTurn(ctx, req) {
		if err != nil {
			errMsg := fmt.Sprintf("❌ Error: %v", err)
			if liveMsgID != "" {
				_ = transport.EditMessage(ctx, req.ChatID, liveMsgID, errMsg)
			} else {
				_, _ = transport.SendInitial(ctx, req.ChatID, replyToMsgID, errMsg)
			}
			return err
		}

		switch ev.Type {
		case react.EventTurnStart:
			if ev.Depth == 0 && ev.Turn > 1 {
				textBuf.Reset()
			}
		case react.EventTextDelta:
			if ev.Depth == 0 {
				textBuf.WriteString(ev.Text)
				flushLive(false)
			}
		case react.EventToolCallReady:
			if ev.ToolCall != nil {
				step := fmt.Sprintf("🔧 `%s`", ev.ToolCall.Name)
				if ev.Depth > 0 {
					step = fmt.Sprintf("↳ [%s] 🔧 `%s`", ev.Agent, ev.ToolCall.Name)
				}
				toolSteps = append(toolSteps, step)
				if len(toolSteps) > 6 {
					toolSteps = toolSteps[len(toolSteps)-6:]
				}
				flushLive(true)
			}
		case react.EventRunEnd:
			if ev.Result != nil && strings.TrimSpace(ev.Result.Output) != "" {
				finalText = strings.TrimSpace(ev.Result.Output)
			}
		}
	}

	if finalText == "" {
		finalText = strings.TrimSpace(textBuf.String())
	}
	if finalText == "" {
		finalText = "(completed with no text output)"
	}

	chunks := ChunkText(finalText, 3800)
	if len(chunks) == 0 {
		chunks = []string{finalText}
	}

	if liveMsgID != "" {
		_ = transport.EditMessage(ctx, req.ChatID, liveMsgID, chunks[0])
		for _, ch := range chunks[1:] {
			if err := transport.SendChunk(ctx, req.ChatID, ch); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := transport.SendInitial(ctx, req.ChatID, replyToMsgID, chunks[0]); err != nil {
		return err
	}
	for _, ch := range chunks[1:] {
		if err := transport.SendChunk(ctx, req.ChatID, ch); err != nil {
			return err
		}
	}
	return nil
}

// ChunkText splits text into chunks of at most maxRunes runes, preferring paragraph/line boundaries.
func ChunkText(s string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = 3800
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return []string{s}
	}

	var out []string
	for len(runes) > maxRunes {
		cut := maxRunes
		for i := maxRunes; i > maxRunes/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		chunk := strings.TrimSpace(string(runes[:cut]))
		if chunk != "" {
			out = append(out, chunk)
		}
		runes = runes[cut:]
	}
	if rem := strings.TrimSpace(string(runes)); rem != "" {
		out = append(out, rem)
	}
	return out
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// IsAllowedUser checks whether userID or username is permitted by the allowlist.
// An empty allowlist permits all users.
func IsAllowedUser(allowlist []string, ids ...string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, rawAllowed := range allowlist {
		allowed := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(rawAllowed)), "@")
		if allowed == "" || allowed == "*" {
			return true
		}
		for _, id := range ids {
			candidate := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "@")
			if candidate != "" && candidate == allowed {
				return true
			}
		}
	}
	return false
}
