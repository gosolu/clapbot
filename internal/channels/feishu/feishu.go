package feishu

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gosolu/clapbot/internal/channels"
	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/runtime"
)

// Bot implements the Feishu / Lark Bot channel using WebSocket long-connection mode
// (no public IP or webhook server required) and pure-Go protobuf frame decoding.
type Bot struct {
	cfg        config.FeishuConfig
	uploadsDir string
	engine     *runtime.Engine
	httpCli    *http.Client
	domain     string

	tokenMu     sync.Mutex
	tenantToken string
	tokenExpiry time.Time

	dedupMu sync.Mutex
	seenIDs map[string]time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var (
	_ runtime.ChannelAdapter    = (*Bot)(nil)
	_ channels.MessageTransport = (*Bot)(nil)
)

// New creates a Feishu / Lark Bot channel adapter.
func New(cfg config.FeishuConfig, uploadsDir string, eng *runtime.Engine) *Bot {
	domain := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	if domain == "" {
		domain = "https://open.feishu.cn"
	}
	return &Bot{
		cfg:        cfg,
		uploadsDir: filepath.Join(uploadsDir, "feishu"),
		engine:     eng,
		httpCli:    &http.Client{Timeout: 30 * time.Second},
		domain:     domain,
		seenIDs:    map[string]time.Time{},
	}
}

// Name implements runtime.ChannelAdapter.
func (b *Bot) Name() string {
	return "feishu"
}

// Start implements runtime.ChannelAdapter.
func (b *Bot) Start(ctx context.Context) error {
	if !b.cfg.Enabled || strings.TrimSpace(b.cfg.AppID) == "" || strings.TrimSpace(b.cfg.AppSecret) == "" {
		return nil
	}
	_ = os.MkdirAll(b.uploadsDir, 0755)
	cctx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancel = cancel
	b.mu.Unlock()

	b.wg.Go(func() {
		b.wsLoop(cctx)
	})
	log.Printf("[feishu] started WebSocket long-connection channel")
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

func (b *Bot) getTenantToken(ctx context.Context) (string, error) {
	b.tokenMu.Lock()
	defer b.tokenMu.Unlock()
	if b.tenantToken != "" && time.Now().Before(b.tokenExpiry) {
		return b.tenantToken, nil
	}

	payload, _ := json.Marshal(map[string]string{
		"app_id":     strings.TrimSpace(b.cfg.AppID),
		"app_secret": strings.TrimSpace(b.cfg.AppSecret),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.domain+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var res struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Code != 0 || res.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu auth error (%d): %s", res.Code, res.Msg)
	}
	b.tenantToken = res.TenantAccessToken
	b.tokenExpiry = time.Now().Add(time.Duration(max(res.Expire-120, 60)) * time.Second)
	return b.tenantToken, nil
}

func (b *Bot) fetchWSEndpoint(ctx context.Context) (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"AppID":     strings.TrimSpace(b.cfg.AppID),
		"AppSecret": strings.TrimSpace(b.cfg.AppSecret),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.domain+"/callback/ws/endpoint", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("locale", "zh")
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URL string `json:"URL"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Code != 0 || res.Data.URL == "" {
		return "", fmt.Errorf("feishu ws endpoint error (%d): %s", res.Code, res.Msg)
	}
	return res.Data.URL, nil
}

func (b *Bot) wsLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		wsURL, err := b.fetchWSEndpoint(ctx)
		if err != nil {
			log.Printf("[feishu] fetch ws endpoint failed: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		if err := b.runWSConnection(ctx, wsURL); err != nil && ctx.Err() == nil {
			log.Printf("[feishu] ws connection closed: %v (reconnecting)", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (b *Bot) runWSConnection(ctx context.Context, wsURL string) error {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if msgType == websocket.MessageText {
			b.handleEventJSON(ctx, data)
			continue
		}
		// Decode Feishu pbbp2 protobuf binary frame
		frame, err := decodePBFrame(data)
		if err != nil {
			continue
		}
		// Method == 1 is DATA frame (event); send ACK frame back immediately so Feishu doesn't redeliver
		if frame.Method == 1 {
			ackPayload, _ := json.Marshal(map[string]int{"code": 200})
			ackFrame := frame
			ackFrame.Payload = ackPayload
			if ackBytes := encodePBFrame(ackFrame); len(ackBytes) > 0 {
				_ = conn.Write(ctx, websocket.MessageBinary, ackBytes)
			}
			if len(frame.Payload) > 0 {
				b.handleEventJSON(ctx, frame.Payload)
			}
		}
	}
}

type feishuEventEnvelope struct {
	Header struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
				UserID string `json:"user_id"`
			} `json:"sender_id"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			ChatID      string `json:"chat_id"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
		} `json:"message"`
	} `json:"event"`
}

func (b *Bot) handleEventJSON(ctx context.Context, raw []byte) {
	var env feishuEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	if env.Header.EventType != "im.message.receive_v1" {
		return
	}
	msgID := env.Event.Message.MessageID
	if msgID == "" || b.isDuplicate(msgID) {
		return
	}

	openID := env.Event.Sender.SenderID.OpenID
	userID := env.Event.Sender.SenderID.UserID
	chatID := env.Event.Message.ChatID
	if !channels.IsAllowedUser(b.cfg.AllowedUsers, openID, userID, chatID) {
		log.Printf("[feishu] ignored message from unauthorized open_id=%s", openID)
		return
	}

	var contentMap map[string]any
	_ = json.Unmarshal([]byte(env.Event.Message.Content), &contentMap)
	text, _ := contentMap["text"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	go func() {
		sid, err := b.engine.ResolveChannelSession(ctx, "feishu", chatID)
		if err != nil {
			log.Printf("[feishu] resolve session error: %v", err)
			return
		}
		turnReq := runtime.TurnRequest{
			SessionID:        sid,
			Channel:          "feishu",
			ChatID:           chatID,
			Input:            text,
			DefaultWorkspace: b.engine.Config().Workspace,
		}
		_ = channels.ExecuteChannelTurn(ctx, b.engine, b, turnReq, msgID, b.cfg.StreamMode)
	}()
}

func (b *Bot) isDuplicate(msgID string) bool {
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	if _, seen := b.seenIDs[msgID]; seen {
		return true
	}
	now := time.Now()
	for k, t := range b.seenIDs {
		if now.Sub(t) > 10*time.Minute {
			delete(b.seenIDs, k)
		}
	}
	b.seenIDs[msgID] = now
	return false
}

// SendTyping implements channels.MessageTransport.
func (b *Bot) SendTyping(_ context.Context, _ string) {}

// SendInitial implements channels.MessageTransport.
func (b *Bot) SendInitial(ctx context.Context, chatID, _, text string) (string, error) {
	return b.sendMessage(ctx, chatID, text)
}

// EditMessage implements channels.MessageTransport.
func (b *Bot) EditMessage(ctx context.Context, _, messageID, text string) error {
	tok, err := b.getTenantToken(ctx)
	if err != nil {
		return err
	}
	contentJSON, _ := json.Marshal(map[string]string{"text": text})
	payload, _ := json.Marshal(map[string]string{
		"msg_type": "text",
		"content":  string(contentJSON),
	})
	u := fmt.Sprintf("%s/open-apis/im/v1/messages/%s", b.domain, messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fmt.Errorf("feishu editMessage decode: %w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("feishu editMessage error (%d): %s", res.Code, res.Msg)
	}
	return nil
}

// SendChunk implements channels.MessageTransport.
func (b *Bot) SendChunk(ctx context.Context, chatID, text string) error {
	_, err := b.sendMessage(ctx, chatID, text)
	return err
}

// SendText implements runtime.ChannelAdapter.
func (b *Bot) SendText(ctx context.Context, chatID, text string) error {
	for _, ch := range channels.ChunkText(text, 3800) {
		if _, err := b.sendMessage(ctx, chatID, ch); err != nil {
			return err
		}
	}
	return nil
}

// SendFile implements runtime.ChannelAdapter.
func (b *Bot) SendFile(ctx context.Context, chatID, filePath, caption string) error {
	tok, err := b.getTenantToken(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("file_type", "stream")
	_ = mw.WriteField("file_name", filepath.Base(filePath))
	fw, err := mw.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.domain+"/open-apis/im/v1/files", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var uploadRes struct {
		Code int `json:"code"`
		Data struct {
			FileKey string `json:"file_key"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploadRes); err != nil || uploadRes.Data.FileKey == "" {
		return fmt.Errorf("feishu upload file failed")
	}

	contentJSON, _ := json.Marshal(map[string]string{"file_key": uploadRes.Data.FileKey})
	if _, err := b.sendRawMessage(ctx, chatID, "file", string(contentJSON)); err != nil {
		return err
	}
	if caption != "" {
		_, _ = b.sendMessage(ctx, chatID, caption)
	}
	return nil
}

func (b *Bot) sendMessage(ctx context.Context, chatID, text string) (string, error) {
	contentJSON, _ := json.Marshal(map[string]string{"text": text})
	return b.sendRawMessage(ctx, chatID, "text", string(contentJSON))
}

func (b *Bot) sendRawMessage(ctx context.Context, chatID, msgType, content string) (string, error) {
	tok, err := b.getTenantToken(ctx)
	if err != nil {
		return "", err
	}
	receiveIDType := "chat_id"
	if strings.HasPrefix(chatID, "ou_") {
		receiveIDType = "open_id"
	}
	payload, _ := json.Marshal(map[string]string{
		"receive_id": chatID,
		"msg_type":   msgType,
		"content":    content,
	})
	u := fmt.Sprintf("%s/open-apis/im/v1/messages?receive_id_type=%s", b.domain, receiveIDType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpCli.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("feishu sendMessage (%d): %s", res.Code, res.Msg)
	}
	return res.Data.MessageID, nil
}

// --- Minimal pure-Go protobuf codec for Feishu pbbp2.Frame ---

type pbHeader struct {
	Key   string
	Value string
}

type pbFrame struct {
	SeqID           uint64
	LogID           uint64
	Service         int32
	Method          int32
	Headers         []pbHeader
	PayloadEncoding string
	PayloadType     string
	Payload         []byte
}

func decodePBFrame(buf []byte) (pbFrame, error) {
	var f pbFrame
	for len(buf) > 0 {
		tag, n := binary.Uvarint(buf)
		if n <= 0 {
			return f, fmt.Errorf("invalid varint tag")
		}
		buf = buf[n:]
		fieldNum := tag >> 3
		wireType := tag & 7

		switch wireType {
		case 0: // Varint
			val, vn := binary.Uvarint(buf)
			if vn <= 0 {
				return f, fmt.Errorf("invalid varint value")
			}
			buf = buf[vn:]
			switch fieldNum {
			case 1:
				f.SeqID = val
			case 2:
				f.LogID = val
			case 3:
				f.Service = int32(val)
			case 4:
				f.Method = int32(val)
			}
		case 2: // Length-delimited
			l, ln := binary.Uvarint(buf)
			if ln <= 0 || uint64(len(buf)-ln) < l {
				return f, fmt.Errorf("invalid length-delimited field")
			}
			buf = buf[ln:]
			sub := buf[:l]
			buf = buf[l:]
			switch fieldNum {
			case 5:
				hdr := decodePBHeader(sub)
				f.Headers = append(f.Headers, hdr)
			case 6:
				f.PayloadEncoding = string(sub)
			case 7:
				f.PayloadType = string(sub)
			case 8:
				f.Payload = append([]byte(nil), sub...)
			}
		default:
			return f, fmt.Errorf("unsupported wire type %d", wireType)
		}
	}
	return f, nil
}

func decodePBHeader(buf []byte) pbHeader {
	var h pbHeader
	for len(buf) > 0 {
		tag, n := binary.Uvarint(buf)
		if n <= 0 {
			break
		}
		buf = buf[n:]
		if (tag & 7) != 2 {
			break
		}
		l, ln := binary.Uvarint(buf)
		if ln <= 0 || uint64(len(buf)-ln) < l {
			break
		}
		buf = buf[ln:]
		val := string(buf[:l])
		buf = buf[l:]
		switch tag >> 3 {
		case 1:
			h.Key = val
		case 2:
			h.Value = val
		}
	}
	return h
}

func encodePBFrame(f pbFrame) []byte {
	var out []byte
	out = appendVarintField(out, 1, f.SeqID)
	out = appendVarintField(out, 2, f.LogID)
	out = appendVarintField(out, 3, uint64(f.Service))
	out = appendVarintField(out, 4, uint64(f.Method))
	for _, h := range f.Headers {
		var hb []byte
		hb = appendBytesField(hb, 1, []byte(h.Key))
		hb = appendBytesField(hb, 2, []byte(h.Value))
		out = appendBytesField(out, 5, hb)
	}
	if f.PayloadEncoding != "" {
		out = appendBytesField(out, 6, []byte(f.PayloadEncoding))
	}
	if f.PayloadType != "" {
		out = appendBytesField(out, 7, []byte(f.PayloadType))
	}
	if len(f.Payload) > 0 {
		out = appendBytesField(out, 8, f.Payload)
	}
	return out
}

func appendVarintField(dst []byte, fieldNum uint64, val uint64) []byte {
	dst = binary.AppendUvarint(dst, fieldNum<<3)
	dst = binary.AppendUvarint(dst, val)
	return dst
}

func appendBytesField(dst []byte, fieldNum uint64, b []byte) []byte {
	dst = binary.AppendUvarint(dst, (fieldNum<<3)|2)
	dst = binary.AppendUvarint(dst, uint64(len(b)))
	dst = append(dst, b...)
	return dst
}
