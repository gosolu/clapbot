package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/gosolu/clapbot/internal/tools/channel"
	"github.com/gosolu/react"
)

// ChannelAdapter represents an external chat channel (Telegram, Feishu, CLI, etc.)
// that can send outbound messages/files and receive turn notifications.
type ChannelAdapter interface {
	Name() string
	Start(ctx context.Context) error
	Stop() error
	SendText(ctx context.Context, chatID, text string) error
	SendFile(ctx context.Context, chatID, filePath, caption string) error
}

// TurnEvent wraps a react.Event with session and channel routing metadata for Gateway/WS subscribers.
type TurnEvent struct {
	SessionID string      `json:"session_id"`
	Channel   string      `json:"channel,omitempty"`
	ChatID    string      `json:"chat_id,omitempty"`
	Event     react.Event `json:"event"`
}

// Bus coordinates registered channel adapters and live event subscribers.
type Bus struct {
	mu       sync.RWMutex
	channels map[string]ChannelAdapter
	subs     map[uint64]chan TurnEvent
	nextSub  uint64
}

var _ channel.Sender = (*Bus)(nil)

// NewBus creates a new message and event bus.
func NewBus() *Bus {
	return &Bus{
		channels: map[string]ChannelAdapter{},
		subs:     map[uint64]chan TurnEvent{},
	}
}

// RegisterChannel registers a channel adapter by name ("telegram", "feishu", "cli", etc.).
func (b *Bus) RegisterChannel(ch ChannelAdapter) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.channels[ch.Name()] = ch
}

// Channels returns all registered channel adapters.
func (b *Bus) Channels() []ChannelAdapter {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]ChannelAdapter, 0, len(b.channels))
	for _, c := range b.channels {
		out = append(out, c)
	}
	return out
}

// SendText implements channel.Sender.
func (b *Bus) SendText(ctx context.Context, channelName, chatID, text string) error {
	b.mu.RLock()
	ch, ok := b.channels[channelName]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("channel %q is not active", channelName)
	}
	return ch.SendText(ctx, chatID, text)
}

// SendFile implements channel.Sender.
func (b *Bus) SendFile(ctx context.Context, channelName, chatID, filePath, caption string) error {
	b.mu.RLock()
	ch, ok := b.channels[channelName]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("channel %q is not active", channelName)
	}
	return ch.SendFile(ctx, chatID, filePath, caption)
}

// Subscribe registers a listener channel for live TurnEvents across sessions.
func (b *Bus) Subscribe() (<-chan TurnEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextSub++
	id := b.nextSub
	ch := make(chan TurnEvent, 128)
	b.subs[id] = ch
	unsub := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
	return ch, unsub
}

// Publish broadcasts a TurnEvent to all active subscribers without blocking.
func (b *Bus) Publish(ev TurnEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
