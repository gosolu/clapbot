package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/react"
)

// WSFrame is the JSON frame sent from Server to Client over /ws.
type WSFrame struct {
	Type         string                 `json:"type"` // "event" | "command" | "done" | "error"
	EventType    react.EventType        `json:"event_type,omitempty"`
	Agent        string                 `json:"agent,omitempty"`
	Depth        int                    `json:"depth,omitempty"`
	Turn         int                    `json:"turn,omitempty"`
	Text         string                 `json:"text,omitempty"`
	ToolName     string                 `json:"tool_name,omitempty"`
	ToolArgs     string                 `json:"tool_args,omitempty"`
	ToolResult   string                 `json:"tool_result,omitempty"`
	ToolIsError  bool                   `json:"tool_is_error,omitempty"`
	Output       string                 `json:"output,omitempty"`
	Command      *runtime.CommandResult `json:"command,omitempty"`
	ErrorMessage string                 `json:"error,omitempty"`
}

// Server exposes the local HTTP and WebSocket Gateway for Clapbot.
type Server struct {
	addr   string
	engine *runtime.Engine
	httpSv *http.Server
	mu     sync.Mutex
	ln     net.Listener
}

// NewServer creates a Gateway Server bound to addr (e.g. "127.0.0.1:8765").
func NewServer(addr string, eng *runtime.Engine) *Server {
	s := &Server{
		addr:   addr,
		engine: eng,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/sessions", s.auth(s.handleListSessions))
	mux.HandleFunc("GET /api/profiles", s.auth(s.handleListProfiles))
	mux.HandleFunc("POST /api/command", s.auth(s.handleCommand))
	mux.HandleFunc("POST /api/turn", s.auth(s.handleTurn))
	mux.HandleFunc("/ws", s.auth(s.handleWS))

	s.httpSv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := ""
		if s.engine != nil && s.engine.Config() != nil {
			expected = strings.TrimSpace(s.engine.Config().GatewayToken)
		}
		if expected != "" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				token = r.URL.Query().Get("token")
			}
			if token != expected {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized: invalid or missing gateway token"})
				return
			}
		}
		next(w, r)
	}
}

// Start begins listening and serving HTTP/WS requests in the background.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("gateway listen %s: %w", s.addr, err)
	}
	s.ln = ln
	go func() {
		_ = s.httpSv.Serve(ln)
	}()
	return nil
}

// Addr returns the bound listener address.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

// Shutdown gracefully stops the HTTP/WS server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSv.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"home_dir":        s.engine.Config().HomeDir,
		"default_profile": s.engine.Config().DefaultProfile,
		"workspace":       s.engine.Config().Workspace,
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.engine.Store().ListSessions(r.Context(), 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) handleListProfiles(w http.ResponseWriter, _ *http.Request) {
	type profileInfo struct {
		Alias    string `json:"alias"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	var out []profileInfo
	for _, alias := range s.engine.Router().Profiles() {
		pcfg, _ := s.engine.Router().Describe(alias)
		out = append(out, profileInfo{
			Alias:    alias,
			Provider: pcfg.Provider,
			Model:    pcfg.Model,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_profile": s.engine.Router().DefaultProfile(),
		"profiles":        out,
	})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	var req runtime.TurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	res, err := s.engine.HandleCommand(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	var req runtime.TurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	res, cmdRes, err := s.engine.RunTurn(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if cmdRes.Handled {
		writeJSON(w, http.StatusOK, map[string]any{"command": cmdRes})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"output": res.Output,
		"turns":  res.Turns,
		"usage":  res.Usage,
	})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx := r.Context()
	for {
		var req runtime.TurnRequest
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			return
		}

		cmdRes, err := s.engine.HandleCommand(ctx, req)
		if err != nil {
			_ = wsjson.Write(ctx, conn, WSFrame{Type: "error", ErrorMessage: err.Error()})
			continue
		}
		if cmdRes.Handled {
			_ = wsjson.Write(ctx, conn, WSFrame{Type: "command", Command: &cmdRes})
			continue
		}

		for ev, err := range s.engine.StreamTurn(ctx, req) {
			if err != nil {
				_ = wsjson.Write(ctx, conn, WSFrame{Type: "error", ErrorMessage: err.Error()})
				break
			}
			frame := WSFrame{
				Type:      "event",
				EventType: ev.Type,
				Agent:     ev.Agent,
				Depth:     ev.Depth,
				Turn:      ev.Turn,
				Text:      ev.Text,
			}
			if ev.ToolCall != nil {
				frame.ToolName = ev.ToolCall.Name
				frame.ToolArgs = string(ev.ToolCall.Arguments)
			}
			if ev.ToolResult != nil {
				frame.ToolResult = ev.ToolResult.Text()
				frame.ToolIsError = ev.ToolResult.IsError
			}
			if ev.Result != nil {
				frame.Output = ev.Result.Output
			}
			if err := wsjson.Write(ctx, conn, frame); err != nil {
				break
			}
		}
		_ = wsjson.Write(ctx, conn, WSFrame{Type: "done"})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
