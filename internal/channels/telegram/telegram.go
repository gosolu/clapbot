package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosolu/clapbot/internal/channels"
	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/react"
)

// Bot implements the Telegram Bot channel over pure HTTP long-polling.
type Bot struct {
	cfg        config.TelegramConfig
	uploadsDir string
	engine     *runtime.Engine
	httpCli    *http.Client
	apiBase    string

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var (
	_ runtime.ChannelAdapter    = (*Bot)(nil)
	_ channels.MessageTransport = (*Bot)(nil)
)

// New creates a Telegram Bot adapter.
func New(cfg config.TelegramConfig, uploadsDir string, eng *runtime.Engine) *Bot {
	token := strings.TrimSpace(cfg.BotToken)
	return &Bot{
		cfg:        cfg,
		uploadsDir: filepath.Join(uploadsDir, "telegram"),
		engine:     eng,
		httpCli:    &http.Client{Timeout: 40 * time.Second},
		apiBase:    "https://api.telegram.org/bot" + token,
	}
}

// Name implements runtime.ChannelAdapter.
func (b *Bot) Name() string {
	return "telegram"
}

// Start implements runtime.ChannelAdapter.
func (b *Bot) Start(ctx context.Context) error {
	if !b.cfg.Enabled || strings.TrimSpace(b.cfg.BotToken) == "" {
		return nil
	}
	_ = os.MkdirAll(b.uploadsDir, 0755)
	cctx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancel = cancel
	b.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.pollLoop(cctx)
	}()
	log.Printf("[telegram] started long-polling channel")
	return nil
}

// Stop implements runtime.ChannelAdapter.
func (b *Bot) Stop() error {
	b.mu.Lock()
	cancel := b.cancel
	b.cancel = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	b.wg.Wait()
	return nil
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	From      *tgUser `json:"from"`
	Chat      tgChat  `json:"chat"`
	Text      string  `json:"text"`
	Caption   string  `json:"caption"`
	Photo     []struct {
		FileID   string `json:"file_id"`
		FileSize int    `json:"file_size"`
	} `json:"photo"`
	Document *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
	} `json:"document"`
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

func (b *Bot) pollLoop(ctx context.Context) {
	var offset int64
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[telegram] getUpdates error: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message == nil {
				continue
			}
			msg := u.Message
			var uidStr, uname string
			if msg.From != nil {
				uidStr = strconv.FormatInt(msg.From.ID, 10)
				uname = msg.From.Username
			}
			chatIDStr := strconv.FormatInt(msg.Chat.ID, 10)
			if !channels.IsAllowedUser(b.cfg.AllowedUsers, uidStr, uname, chatIDStr) {
				log.Printf("[telegram] ignored message from unauthorized user id=%s username=%s", uidStr, uname)
				continue
			}

			go b.handleInbound(ctx, msg)
		}
	}
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	payload, _ := json.Marshal(map[string]any{
		"offset":          offset,
		"timeout":         25,
		"allowed_updates": []string{"message"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBase+"/getUpdates", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var res struct {
		OK          bool       `json:"ok"`
		Description string     `json:"description"`
		Result      []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", res.Description)
	}
	return res.Result, nil
}

func (b *Bot) handleInbound(ctx context.Context, msg *tgMessage) {
	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}

	var parts []react.Part
	if len(msg.Photo) > 0 {
		bestPhoto := msg.Photo[len(msg.Photo)-1]
		if data, localPath, err := b.downloadFile(ctx, bestPhoto.FileID, fmt.Sprintf("photo_%d.jpg", msg.MessageID)); err == nil {
			parts = append(parts, react.ImageDataPart("image/jpeg", data))
			if text == "" {
				text = fmt.Sprintf("[Attached image saved at %s]", localPath)
			} else {
				text += fmt.Sprintf("\n[Attached image saved at %s]", localPath)
			}
		}
	}
	if msg.Document != nil {
		fname := msg.Document.FileName
		if fname == "" {
			fname = fmt.Sprintf("doc_%d", msg.MessageID)
		}
		if _, localPath, err := b.downloadFile(ctx, msg.Document.FileID, fname); err == nil {
			if text == "" {
				text = fmt.Sprintf("Please inspect the attached file at %s", localPath)
			} else {
				text += fmt.Sprintf("\n[Attached file saved at %s]", localPath)
			}
		}
	}

	if text == "" && len(parts) == 0 {
		return
	}

	sid, err := b.engine.ResolveChannelSession(ctx, "telegram", chatID)
	if err != nil {
		log.Printf("[telegram] resolve session error: %v", err)
		return
	}

	turnReq := runtime.TurnRequest{
		SessionID:        sid,
		Channel:          "telegram",
		ChatID:           chatID,
		Input:            text,
		Parts:            parts,
		DefaultWorkspace: b.engine.Config().Workspace,
	}

	_ = channels.ExecuteChannelTurn(
		ctx,
		b.engine,
		b,
		turnReq,
		strconv.FormatInt(msg.MessageID, 10),
		b.cfg.StreamMode,
	)
}

func (b *Bot) downloadFile(ctx context.Context, fileID, fallbackName string) ([]byte, string, error) {
	payload, _ := json.Marshal(map[string]any{"file_id": fileID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBase+"/getFile", bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var meta struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil || !meta.OK || meta.Result.FilePath == "" {
		return nil, "", fmt.Errorf("getFile failed")
	}

	dlURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", strings.TrimSpace(b.cfg.BotToken), meta.Result.FilePath)
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, "", err
	}
	dlResp, err := b.httpCli.Do(dlReq)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = dlResp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(dlResp.Body, 20*1024*1024))
	if err != nil {
		return nil, "", err
	}
	localPath := filepath.Join(b.uploadsDir, filepath.Base(fallbackName))
	_ = os.WriteFile(localPath, data, 0644)
	return data, localPath, nil
}

// SendTyping implements channels.MessageTransport.
func (b *Bot) SendTyping(ctx context.Context, chatID string) {
	_, _ = b.callJSON(ctx, "sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  "typing",
	})
}

// SendInitial implements channels.MessageTransport.
func (b *Bot) SendInitial(ctx context.Context, chatID, replyToMsgID, text string) (string, error) {
	params := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if replyToMsgID != "" {
		if idNum, err := strconv.ParseInt(replyToMsgID, 10, 64); err == nil {
			params["reply_to_message_id"] = idNum
		}
	}
	raw, err := b.callJSON(ctx, "sendMessage", params)
	if err != nil {
		return "", err
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &msg)
	return strconv.FormatInt(msg.MessageID, 10), nil
}

// EditMessage implements channels.MessageTransport.
func (b *Bot) EditMessage(ctx context.Context, chatID, messageID, text string) error {
	mid, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return err
	}
	_, err = b.callJSON(ctx, "editMessageText", map[string]any{
		"chat_id":    chatID,
		"message_id": mid,
		"text":       text,
	})
	return err
}

// SendChunk implements channels.MessageTransport.
func (b *Bot) SendChunk(ctx context.Context, chatID, text string) error {
	_, err := b.callJSON(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	return err
}

// SendText implements runtime.ChannelAdapter.
func (b *Bot) SendText(ctx context.Context, chatID, text string) error {
	for _, ch := range channels.ChunkText(text, 3800) {
		if err := b.SendChunk(ctx, chatID, ch); err != nil {
			return err
		}
	}
	return nil
}

// SendFile implements runtime.ChannelAdapter.
func (b *Bot) SendFile(ctx context.Context, chatID, filePath, caption string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", chatID)
	if caption != "" {
		_ = mw.WriteField("caption", caption)
	}
	fw, err := mw.CreateFormFile("document", filepath.Base(filePath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBase+"/sendDocument", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		bErr, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendDocument HTTP %d: %s", resp.StatusCode, string(bErr))
	}
	return nil
}

func (b *Bot) callJSON(ctx context.Context, method string, payload map[string]any) (json.RawMessage, error) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBase+"/"+method, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var res struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, res.Description)
	}
	return res.Result, nil
}
