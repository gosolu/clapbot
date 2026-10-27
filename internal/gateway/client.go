package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/httpr"
)

// Client talks to a running local Gateway daemon over HTTP and WebSocket.
type Client struct {
	baseURL string
	wsURL   string
	token   string
}

// NewClient constructs a Gateway Client for the given host:port address and optional auth token.
func NewClient(addr string, token ...string) *Client {
	addr = strings.TrimSpace(addr)
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")
	wsURL := "ws" + strings.TrimPrefix(addr, "http") + "/ws"

	var tok string
	if len(token) > 0 {
		tok = strings.TrimSpace(token[0])
	}

	return &Client{
		baseURL: addr,
		wsURL:   wsURL,
		token:   tok,
	}
}

func (c *Client) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// Ping checks whether the local Gateway daemon is alive.
func (c *Client) Ping(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	c.setAuth(req)
	resp, err := httpr.Do(ctx, req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// ListSessions fetches recent sessions from the Gateway daemon.
func (c *Client) ListSessions(ctx context.Context) ([]store.Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/sessions", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := httpr.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("gateway: unauthorized")
	}
	var out struct {
		Sessions []store.Session `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// ProfileItem describes a configured model profile returned by the Gateway.
type ProfileItem struct {
	Alias    string `json:"alias"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ListProfiles fetches model profiles from the Gateway daemon.
func (c *Client) ListProfiles(ctx context.Context) ([]ProfileItem, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/profiles", nil)
	if err != nil {
		return nil, "", err
	}
	c.setAuth(req)
	resp, err := httpr.Do(ctx, req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, "", errors.New("gateway: unauthorized")
	}
	var out struct {
		DefaultProfile string        `json:"default_profile"`
		Profiles       []ProfileItem `json:"profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", err
	}
	return out.Profiles, out.DefaultProfile, nil
}

// Command executes a slash command on the Gateway daemon.
func (c *Client) Command(ctx context.Context, turnReq runtime.TurnRequest) (runtime.CommandResult, error) {
	b, _ := json.Marshal(turnReq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/command", bytes.NewReader(b))
	if err != nil {
		return runtime.CommandResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := httpr.Do(ctx, req)
	if err != nil {
		return runtime.CommandResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return runtime.CommandResult{}, errors.New("gateway: unauthorized")
	}
	var cmdRes runtime.CommandResult
	if err := json.NewDecoder(resp.Body).Decode(&cmdRes); err != nil {
		return runtime.CommandResult{}, err
	}
	return cmdRes, nil
}

// StreamTurn streams a turn over WebSocket from the Gateway daemon.
func (c *Client) StreamTurn(ctx context.Context, turnReq runtime.TurnRequest) iter.Seq2[WSFrame, error] {
	return func(yield func(WSFrame, error) bool) {
		var dialOpts *websocket.DialOptions
		if c.token != "" {
			dialOpts = &websocket.DialOptions{
				HTTPHeader: http.Header{
					"Authorization": []string{"Bearer " + c.token},
				},
			}
		}
		conn, _, err := websocket.Dial(ctx, c.wsURL, dialOpts)
		if err != nil {
			yield(WSFrame{}, fmt.Errorf("gateway dial %s: %w", c.wsURL, err))
			return
		}
		defer func() { _ = conn.CloseNow() }()

		if err := wsjson.Write(ctx, conn, turnReq); err != nil {
			yield(WSFrame{}, err)
			return
		}

		for {
			var frame WSFrame
			if err := wsjson.Read(ctx, conn, &frame); err != nil {
				yield(WSFrame{}, err)
				return
			}
			if frame.Type == "error" {
				yield(frame, errors.New(frame.ErrorMessage))
				return
			}
			if frame.Type == "done" {
				return
			}
			if !yield(frame, nil) {
				return
			}
		}
	}
}
