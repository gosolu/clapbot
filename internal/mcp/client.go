package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosolu/clapbot/internal/config"
)

// ToolDef represents an MCP tool returned by tools/list.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Client is a JSON-RPC 2.0 MCP client supporting stdio and HTTP transports.
type Client struct {
	name string
	cfg  config.MCPServerConfig

	nextID atomic.Int64

	// stdio transport fields
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	pending sync.Map // map[int64]chan rpcResponse

	// HTTP transport fields
	httpClient *http.Client
	sessionID  string
}

// Connect establishes an MCP session and runs the initialize handshake.
func Connect(ctx context.Context, name string, cfg config.MCPServerConfig) (*Client, error) {
	c := &Client{
		name:       name,
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	if strings.TrimSpace(cfg.URL) != "" {
		if err := c.initHandshake(ctx); err != nil {
			return nil, err
		}
		return c, nil
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("mcp server %q requires either command or url", name)
	}
	if err := c.startStdio(ctx); err != nil {
		return nil, err
	}
	if err := c.initHandshake(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) startStdio(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.cfg.Command, c.cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range c.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp %s start %q: %w", c.name, c.cfg.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin

	go c.readLoop(stdout)
	return nil
}

func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID != 0 {
			if chAny, ok := c.pending.LoadAndDelete(resp.ID); ok {
				ch := chAny.(chan rpcResponse)
				ch <- resp
				close(ch)
			}
		}
	}
	// Fail any pending calls on EOF
	c.pending.Range(func(key, value any) bool {
		c.pending.Delete(key)
		ch := value.(chan rpcResponse)
		close(ch)
		return true
	})
}

func (c *Client) initHandshake(ctx context.Context) error {
	initParams := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "clapbot",
			"version": "1.0.0",
		},
	}
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if _, err := c.call(hctx, "initialize", initParams); err != nil {
		return fmt.Errorf("mcp %s initialize: %w", c.name, err)
	}
	_ = c.notify(hctx, "notifications/initialized", map[string]any{})
	return nil
}

// ListTools fetches the tools exposed by the MCP server.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return parsed.Tools, nil
}

// CallTool invokes an MCP tool by name with JSON arguments.
func (c *Client) CallTool(ctx context.Context, toolName string, args json.RawMessage) (string, bool, error) {
	var argMap any = map[string]any{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &argMap)
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": argMap,
	})
	if err != nil {
		return "", true, err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return string(raw), false, nil
	}
	var parts []string
	for _, p := range res.Content {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n"), res.IsError, nil
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	if c.cfg.URL != "" {
		_, _ = c.callHTTP(ctx, req)
		return nil
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if c.cfg.URL != "" {
		return c.callHTTP(ctx, req)
	}

	ch := make(chan rpcResponse, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.writeMu.Lock()
	_, err = c.stdin.Write(append(b, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp %s connection closed", c.name)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp %s rpc error (%d): %s", c.name, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) callHTTP(ctx context.Context, rpcReq rpcRequest) (json.RawMessage, error) {
	b, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	if rpcReq.ID == 0 {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("mcp %s http decode: %w", c.name, err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("mcp %s rpc error (%d): %s", c.name, rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// Close terminates the MCP connection or subprocess.
func (c *Client) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}
