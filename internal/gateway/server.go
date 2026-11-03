package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/superagent"
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
	mux.HandleFunc("GET /api/tickets", s.auth(s.handleListTickets))
	mux.HandleFunc("GET /api/tickets/{id}", s.auth(s.handleGetTicket))
	mux.HandleFunc("POST /api/tickets/{id}/stop", s.auth(s.handleStopTicket))
	mux.HandleFunc("POST /api/tickets/{id}/retry", s.auth(s.handleRetryTicket))
	mux.HandleFunc("GET /api/attention", s.auth(s.handleAttention))
	mux.HandleFunc("GET /api/decisions", s.auth(s.handleListDecisions))
	mux.HandleFunc("POST /api/decisions/{id}/dismiss", s.auth(s.handleDismissDecision))
	mux.HandleFunc("GET /api/overview", s.auth(s.handleOverview))
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
		slog.ErrorContext(r.Context(), "HTTP command error", "error", err)
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
		slog.ErrorContext(r.Context(), "HTTP turn error", "session_id", req.SessionID, "error", err)
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
		slog.ErrorContext(r.Context(), "websocket upgrade failed", "error", err)
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
			slog.ErrorContext(ctx, "WS command error", "session_id", req.SessionID, "error", err)
			_ = wsjson.Write(ctx, conn, WSFrame{Type: "error", ErrorMessage: err.Error()})
			continue
		}
		if cmdRes.Handled {
			_ = wsjson.Write(ctx, conn, WSFrame{Type: "command", Command: &cmdRes})
			continue
		}

		for ev, err := range s.engine.StreamTurn(ctx, req) {
			if err != nil {
				slog.ErrorContext(ctx, "WS stream turn error", "session_id", req.SessionID, "error", err)
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

func (s *Server) requireSuperAgent(w http.ResponseWriter) *superagent.Engine {
	if s.engine == nil || !s.engine.IsSuperAgent() || s.engine.SuperAgent() == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "superagent mode is not active"})
		return nil
	}
	return s.engine.SuperAgent()
}

func (s *Server) handleListTickets(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	state := r.URL.Query().Get("state") // "open" (default), "ended", "all"
	stageFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("stage")))

	var status superagent.TicketStatus
	switch strings.ToLower(state) {
	case "ended":
		status = superagent.TicketEnded
	case "all":
		status = ""
	default:
		status = superagent.TicketOpen
	}

	tickets, err := sa.ListTickets(r.Context(), status)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	var out []superagent.TicketSummaryView
	for _, t := range tickets {
		var as *superagent.Assignment
		if t.AssignmentID != "" {
			as, _ = sa.GetAssignment(r.Context(), t.AssignmentID)
		}
		stage := superagent.ComputeStage(t, as)

		// Filter by stage if requested
		if stageFilter != "" && stageFilter != "all" {
			if stageFilter == "person" && !stage.HumanAttention() {
				continue
			}
			if stageFilter == "working" && stage != superagent.StageWorking && stage != superagent.StageWorkingQueued {
				continue
			}
			if stageFilter == "idle" && stage != superagent.StageIdle {
				continue
			}
		}

		out = append(out, superagent.TicketSummaryView{
			ID:             t.ID,
			Name:           t.Name,
			Title:          t.Title,
			Status:         t.Status,
			Stage:          stage,
			HumanAttention: stage.HumanAttention(),
			Attention:      t.Attention,
			AssignmentID:   t.AssignmentID,
			Requester:      t.Mandate.Requester.Label(),
			Channels:       t.Channels,
			CreatedAt:      t.CreatedAt,
			UpdatedAt:      t.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tickets": out,
		"total":   len(out),
	})
}

func (s *Server) handleGetTicket(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	id := r.PathValue("id")
	detail, err := sa.GetTicketDetail(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleStopTicket(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	reason := req.Reason
	if reason == "" {
		reason = "stopped via Gateway API"
	}

	if err := sa.StopTicket(r.Context(), id, reason); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "ticket_id": id})
}

func (s *Server) handleRetryTicket(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	id := r.PathValue("id")
	if err := sa.RetryTicket(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"retried": true, "ticket_id": id})
}

func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	att, err := sa.GetAttention(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, att)
}

func (s *Server) handleListDecisions(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	outcome := superagent.Outcome(r.URL.Query().Get("outcome"))
	limit := 50
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}
	list, err := sa.ListDecisions(r.Context(), outcome, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"decisions": list,
		"total":     len(list),
	})
}

func (s *Server) handleDismissDecision(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	id := r.PathValue("id")
	if err := sa.Dismiss(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": true, "decision_id": id})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	sa := s.requireSuperAgent(w)
	if sa == nil {
		return
	}
	ov, err := sa.GetOverview(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ov)
}
