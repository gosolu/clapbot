package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gosolu/clapbot/internal/config"
)

// CommandResult represents the outcome of a slash command.
type CommandResult struct {
	Handled      bool
	Output       string
	NewSessionID string // Non-empty if the command switched or created a session
	Exit         bool   // True for /exit or /quit in CLI
	Detach       bool   // True for /detach in CLI
	Interactive  string // "sessions" or "model" when CLI should open a huh picker
}

// NewSessionID generates a short readable session ID.
func NewSessionID(prefix string) string {
	if prefix == "" {
		prefix = "s"
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s_%s_%s", prefix, time.Now().Format("0102"), hex.EncodeToString(b[:]))
}

// HandleCommand processes slash commands across CLI, Telegram, and Feishu.
func (e *Engine) HandleCommand(ctx context.Context, req TurnRequest) (CommandResult, error) {
	input := strings.TrimSpace(req.Input)
	if !strings.HasPrefix(input, "/") {
		return CommandResult{}, nil
	}

	fields := strings.Fields(input)
	cmd := strings.ToLower(fields[0])
	// Strip @botname suffix from Telegram group slash commands (e.g. /status@my_bot)
	if atIdx := strings.IndexByte(cmd, '@'); atIdx > 0 {
		cmd = cmd[:atIdx]
	}
	args := strings.TrimSpace(strings.TrimPrefix(input, fields[0]))

	switch cmd {
	case "/help", "/start":
		return CommandResult{
			Handled: true,
			Output: strings.TrimSpace(`**Clapbot Commands**
- ` + "`/new`" + ` — Start a fresh conversation session
- ` + "`/reset`" + ` — Clear messages in the current session
- ` + "`/sessions [id]`" + ` — List recent sessions or switch to session ` + "`id`" + `
- ` + "`/workspace [path]`" + ` — Show or change the current session's filesystem workspace
- ` + "`/model [profile]`" + ` — Show or switch the model profile
- ` + "`/compact`" + ` — Summarize and compact current conversation history
- ` + "`/schedules`" + ` — List active cron schedules and timers
- ` + "`/tools`" + ` — List registered agent tools
- ` + "`/skills`" + ` — List loaded progressive-disclosure skills
- ` + "`/status`" + ` — Show session, model, workspace, and runtime status
- ` + "`/detach`" + ` — Detach CLI from background Gateway daemon
- ` + "`/exit`" + ` — Exit CLI`),
		}, nil

	case "/exit", "/quit":
		return CommandResult{Handled: true, Output: "Bye!", Exit: true}, nil

	case "/detach":
		return CommandResult{Handled: true, Output: "Detached from gateway session.", Detach: true}, nil

	case "/new":
		prefix := req.Channel
		if prefix == "" {
			prefix = "cli"
		}
		newSID := NewSessionID(prefix)
		ws := req.DefaultWorkspace
		if ws == "" {
			ws = e.cfg.Workspace
		}
		if sess, _ := e.store.GetSession(ctx, req.SessionID); sess != nil && sess.WorkspaceDir != "" {
			ws = sess.WorkspaceDir
		}
		_, err := e.store.EnsureSession(ctx, newSID, ws, e.cfg.DefaultProfile)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if req.Channel != "" && req.ChatID != "" {
			_ = e.store.SetBoundSession(ctx, req.Channel, req.ChatID, newSID)
		}
		return CommandResult{
			Handled:      true,
			NewSessionID: newSID,
			Output:       fmt.Sprintf("Started new session `%s` (workspace: `%s`).", newSID, ws),
		}, nil

	case "/reset", "/clear":
		if err := e.store.ClearMessages(ctx, req.SessionID); err != nil {
			return CommandResult{Handled: true}, err
		}
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Cleared conversation history for session `%s`.", req.SessionID),
		}, nil

	case "/sessions":
		if args == "" {
			sessions, err := e.store.ListSessions(ctx, 20)
			if err != nil {
				return CommandResult{Handled: true}, err
			}
			if len(sessions) == 0 {
				return CommandResult{Handled: true, Output: "No saved sessions yet."}, nil
			}
			var b strings.Builder
			b.WriteString("**Recent Sessions**\n")
			for _, s := range sessions {
				marker := " "
				if s.ID == req.SessionID {
					marker = "*"
				}
				title := s.Title
				if title == "" {
					title = "(untitled)"
				}
				fmt.Fprintf(&b, "%s `%s` — %s (%d msgs, profile: `%s`, workspace: `%s`)\n",
					marker, s.ID, title, s.MessageCount, s.Profile, s.WorkspaceDir)
			}
			b.WriteString("\nUse `/sessions <id>` to switch to any session.")
			return CommandResult{
				Handled:     true,
				Output:      strings.TrimSpace(b.String()),
				Interactive: "sessions",
			}, nil
		}
		targetID := strings.TrimSpace(args)
		sess, err := e.store.GetSession(ctx, targetID)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if sess == nil {
			ws := req.DefaultWorkspace
			if ws == "" {
				ws = e.cfg.Workspace
			}
			sess, err = e.store.EnsureSession(ctx, targetID, ws, e.cfg.DefaultProfile)
			if err != nil {
				return CommandResult{Handled: true}, err
			}
		}
		if req.Channel != "" && req.ChatID != "" {
			_ = e.store.SetBoundSession(ctx, req.Channel, req.ChatID, sess.ID)
		}
		return CommandResult{
			Handled:      true,
			NewSessionID: sess.ID,
			Output:       fmt.Sprintf("Switched to session `%s` (%d messages, workspace: `%s`, profile: `%s`).", sess.ID, sess.MessageCount, sess.WorkspaceDir, sess.Profile),
		}, nil

	case "/workspace", "/cd":
		sess, err := e.ensureSessionForTurn(ctx, req)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if args == "" {
			return CommandResult{
				Handled: true,
				Output:  fmt.Sprintf("Current workspace for session `%s`: `%s`", sess.ID, sess.WorkspaceDir),
			}, nil
		}
		targetDir := config.ExpandPath(args)
		if !filepath.IsAbs(targetDir) {
			targetDir = filepath.Join(sess.WorkspaceDir, targetDir)
		}
		absDir, err := filepath.Abs(targetDir)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		info, err := os.Stat(absDir)
		if err != nil || !info.IsDir() {
			return CommandResult{
				Handled: true,
				Output:  fmt.Sprintf("Error: directory `%s` does not exist.", absDir),
			}, nil
		}
		if err := e.store.UpdateSessionWorkspace(ctx, sess.ID, absDir); err != nil {
			return CommandResult{Handled: true}, err
		}
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Updated workspace for session `%s` to `%s`.", sess.ID, absDir),
		}, nil

	case "/model", "/models":
		sess, err := e.ensureSessionForTurn(ctx, req)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if args == "" {
			var b strings.Builder
			b.WriteString("**Configured Model Profiles**\n")
			for _, alias := range e.router.Profiles() {
				pcfg, _ := e.router.Describe(alias)
				marker := " "
				if alias == sess.Profile {
					marker = "*"
				}
				fmt.Fprintf(&b, "%s `%s` -> %s / `%s`\n", marker, alias, pcfg.Provider, pcfg.Model)
			}
			b.WriteString("\nUse `/model <profile>` to switch.")
			return CommandResult{
				Handled:     true,
				Output:      strings.TrimSpace(b.String()),
				Interactive: "model",
			}, nil
		}
		targetProf := strings.TrimSpace(args)
		if !e.router.HasProfile(targetProf) {
			return CommandResult{
				Handled: true,
				Output:  fmt.Sprintf("Unknown profile `%s`. Available profiles: %s", targetProf, strings.Join(e.router.Profiles(), ", ")),
			}, nil
		}
		if err := e.store.UpdateSessionProfile(ctx, sess.ID, targetProf); err != nil {
			return CommandResult{Handled: true}, err
		}
		pcfg, _ := e.router.Describe(targetProf)
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Switched session `%s` to profile `%s` (%s / `%s`).", sess.ID, targetProf, pcfg.Provider, pcfg.Model),
		}, nil

	case "/compact":
		compacted, before, after, err := e.CompactSession(ctx, req.SessionID)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if !compacted {
			return CommandResult{
				Handled: true,
				Output:  fmt.Sprintf("Session `%s` has %d messages and does not need compaction yet.", req.SessionID, before),
			}, nil
		}
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Compacted session `%s` from %d messages down to %d messages.", req.SessionID, before, after),
		}, nil

	case "/schedules":
		list, err := e.store.ListSchedules(ctx)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if len(list) == 0 {
			return CommandResult{Handled: true, Output: "No active schedules."}, nil
		}
		var b strings.Builder
		b.WriteString("**Scheduled Tasks**\n")
		for _, sch := range list {
			state := "active"
			if !sch.Enabled {
				state = "done"
			}
			spec := sch.CronExpr
			if sch.RunAt != nil {
				spec = sch.RunAt.Format(time.RFC3339)
			}
			fmt.Fprintf(&b, "- `%s` (%s) [%s] `%s` -> %s:%s | %s\n",
				sch.ID, sch.Name, state, spec, sch.Channel, sch.ChatID, sch.Prompt)
		}
		return CommandResult{Handled: true, Output: strings.TrimSpace(b.String())}, nil

	case "/tools":
		specs := e.agent.Tools()
		var b strings.Builder
		fmt.Fprintf(&b, "**Registered Tools (%d)**\n", len(specs))
		for _, sp := range specs {
			fmt.Fprintf(&b, "- `%s`: %s\n", sp.Name, sp.Description)
		}
		return CommandResult{Handled: true, Output: strings.TrimSpace(b.String())}, nil

	case "/skills":
		skills := e.agent.Skills()
		if len(skills) == 0 {
			return CommandResult{Handled: true, Output: "No skills loaded."}, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**Loaded Skills (%d)**\n", len(skills))
		for _, sk := range skills {
			fmt.Fprintf(&b, "- `%s`: %s\n", sk.Name, sk.Description)
		}
		return CommandResult{Handled: true, Output: strings.TrimSpace(b.String())}, nil

	case "/status":
		sess, err := e.ensureSessionForTurn(ctx, req)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		pcfg, _ := e.router.Describe(sess.Profile)
		return CommandResult{
			Handled: true,
			Output: fmt.Sprintf("**Clapbot Status**\n- Session: `%s` (%d messages)\n- Profile: `%s` (%s / `%s`)\n- Workspace: `%s`\n- Home: `%s`\n- Tools: %d | Skills: %d",
				sess.ID, sess.MessageCount, sess.Profile, pcfg.Provider, pcfg.Model, sess.WorkspaceDir, e.cfg.HomeDir, len(e.agent.Tools()), len(e.agent.Skills())),
		}, nil

	default:
		return CommandResult{}, nil
	}
}
