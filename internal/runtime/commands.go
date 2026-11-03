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
	"github.com/gosolu/clapbot/internal/superagent"
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
- ` + "`/policy`" + ` — Show or update super-agent accept mode and rules
- ` + "`/tickets [filter]`" + ` — List super-agent tickets (open, ended, person, working, idle)
- ` + "`/ticket <id|name>`" + ` — View detailed information about a ticket
- ` + "`/attention`" + ` — Show tickets and evaluations requiring human attention
- ` + "`/stop <id|name> [reason]`" + ` — Stop work on a ticket and withdraw its mandate
- ` + "`/retry <id|name>`" + ` — Restart a blocked or failed ticket
- ` + "`/dismiss <id>`" + ` — Dismiss an unavailable evaluation decision
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

	case "/policy":
		if e.saEngine == nil || e.saPolicy == nil {
			return CommandResult{
				Handled: true,
				Output:  "SuperAgent mode is not active.",
			}, nil
		}
		cur := e.saPolicy.CurrentPolicy()
		subFields := strings.Fields(args)
		if len(subFields) == 0 {
			var b strings.Builder
			b.WriteString(fmt.Sprintf("**SuperAgent Policy** (version: `%s`)\n", cur.Version()))
			b.WriteString(fmt.Sprintf("- **Accept Mode**: `%s`\n", cur.Mode()))
			b.WriteString("- **Rules**:\n")
			if len(cur.Rules) == 0 {
				b.WriteString("  - *(none configured)*\n")
			} else {
				for _, r := range cur.Rules {
					status := "disabled"
					if r.Enabled {
						status = "enabled"
					}
					b.WriteString(fmt.Sprintf("  - `%s`: %s — %s\n", r.Name, status, r.Instructions))
				}
			}
			b.WriteString("- **Sources**:\n")
			if len(cur.Sources) == 0 {
				b.WriteString("  - *(none configured)*\n")
			} else {
				for sName, sp := range cur.Sources {
					b.WriteString(fmt.Sprintf("  - **%s**: allowed=[%s] bots=[%s]\n", sName, strings.Join(sp.Allowed, ", "), strings.Join(sp.Bots, ", ")))
				}
			}
			return CommandResult{Handled: true, Output: strings.TrimSpace(b.String())}, nil
		}

		subCmd := strings.ToLower(subFields[0])
		switch subCmd {
		case "mode":
			if len(subFields) < 2 {
				return CommandResult{Handled: true, Output: fmt.Sprintf("Current accept mode: `%s`. Usage: `/policy mode <off|observe|auto>`", cur.Mode())}, nil
			}
			newMode := superagent.AcceptMode(strings.ToLower(subFields[1]))
			switch newMode {
			case superagent.AcceptOff, superagent.AcceptObserve, superagent.AcceptAuto:
			default:
				return CommandResult{Handled: true, Output: "Invalid mode. Must be one of: `off`, `observe`, `auto`"}, nil
			}
			newPol := cur.WithAcceptMode(newMode)
			oldVer := cur.Version()
			if err := e.UpdateSuperAgentPolicy(ctx, newPol); err != nil {
				return CommandResult{Handled: true}, err
			}
			return CommandResult{
				Handled: true,
				Output:  fmt.Sprintf("SuperAgent policy updated (version `%s` -> `%s`): accept mode is now `%s`.", oldVer, newPol.Version(), newMode),
			}, nil

		case "rule":
			if len(subFields) < 3 {
				return CommandResult{Handled: true, Output: "Usage: `/policy rule <name> <enable|disable>`"}, nil
			}
			ruleName := subFields[1]
			enable := strings.EqualFold(subFields[2], "enable") || strings.EqualFold(subFields[2], "true") || strings.EqualFold(subFields[2], "1")
			newPol := cur.WithRuleEnabled(ruleName, enable)
			oldVer := cur.Version()
			if err := e.UpdateSuperAgentPolicy(ctx, newPol); err != nil {
				return CommandResult{Handled: true}, err
			}
			statusStr := "disabled"
			if enable {
				statusStr = "enabled"
			}
			return CommandResult{
				Handled: true,
				Output:  fmt.Sprintf("SuperAgent policy updated (version `%s` -> `%s`): rule `%s` is now %s.", oldVer, newPol.Version(), ruleName, statusStr),
			}, nil

		default:
			return CommandResult{
				Handled: true,
				Output:  "Unknown /policy subcommand. Use `/policy`, `/policy mode <off|observe|auto>`, or `/policy rule <name> <enable|disable>`.",
			}, nil
		}

	case "/tickets":
		if e.saEngine == nil {
			return CommandResult{Handled: true, Output: "SuperAgent mode is not active."}, nil
		}
		filter := strings.ToLower(strings.TrimSpace(args))
		var status superagent.TicketStatus
		switch filter {
		case "ended":
			status = superagent.TicketEnded
		case "all":
			status = ""
		default:
			status = superagent.TicketOpen
		}
		tickets, err := e.saEngine.ListTickets(ctx, status)
		if err != nil {
			return CommandResult{Handled: true}, err
		}

		var filtered []*superagent.Ticket
		for _, t := range tickets {
			var as *superagent.Assignment
			if t.AssignmentID != "" {
				as, _ = e.saEngine.GetAssignment(ctx, t.AssignmentID)
			}
			stage := superagent.ComputeStage(t, as)
			if filter == "person" && !stage.HumanAttention() {
				continue
			}
			if filter == "working" && stage != superagent.StageWorking && stage != superagent.StageWorkingQueued {
				continue
			}
			if filter == "idle" && stage != superagent.StageIdle {
				continue
			}
			filtered = append(filtered, t)
		}

		if len(filtered) == 0 {
			msg := "No tickets found."
			if filter != "" {
				msg = fmt.Sprintf("No tickets found matching filter `%s`.", filter)
			}
			return CommandResult{Handled: true, Output: msg}, nil
		}

		var b strings.Builder
		header := "open"
		if filter != "" {
			header = filter
		}
		fmt.Fprintf(&b, "**Tickets (%s, %d)**\n", header, len(filtered))
		for _, t := range filtered {
			var as *superagent.Assignment
			if t.AssignmentID != "" {
				as, _ = e.saEngine.GetAssignment(ctx, t.AssignmentID)
			}
			stage := superagent.ComputeStage(t, as)
			warn := ""
			if stage.HumanAttention() {
				warn = " ⚠️"
			}
			fmt.Fprintf(&b, "- `%s` [%s%s]: %s (ID: `%s`)\n", t.Name, stage, warn, t.Title, t.ID)
			if t.Attention != "" {
				fmt.Fprintf(&b, "  *Attention*: %s\n", t.Attention)
			} else if t.LastReport != nil && t.LastReport.State != superagent.ReportProgress {
				fmt.Fprintf(&b, "  *Report*: [%s] %s\n", t.LastReport.State, t.LastReport.Summary)
			}
		}
		return CommandResult{Handled: true, Output: strings.TrimSpace(b.String())}, nil

	case "/ticket":
		if e.saEngine == nil {
			return CommandResult{Handled: true, Output: "SuperAgent mode is not active."}, nil
		}
		query := strings.TrimSpace(args)
		if query == "" {
			return CommandResult{Handled: true, Output: "Usage: `/ticket <id|name>`"}, nil
		}
		t, err := e.saEngine.FindTicketByIDOrName(ctx, query)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if t == nil {
			return CommandResult{Handled: true, Output: fmt.Sprintf("Ticket `%s` not found.", query)}, nil
		}
		detail, err := e.saEngine.GetTicketDetail(ctx, t.ID)
		if err != nil {
			return CommandResult{Handled: true}, err
		}

		var b strings.Builder
		fmt.Fprintf(&b, "**Ticket `%s`** (%s)\n", detail.Ticket.Name, detail.Ticket.ID)
		fmt.Fprintf(&b, "- **Title**: %s\n", detail.Ticket.Title)
		fmt.Fprintf(&b, "- **Status**: `%s`\n", detail.Ticket.Status)
		fmt.Fprintf(&b, "- **Stage**: `%s`", detail.Stage)
		if detail.HumanAttention {
			b.WriteString(" ⚠️ *(requires human attention)*")
		}
		b.WriteString("\n")
		if detail.Ticket.Attention != "" {
			fmt.Fprintf(&b, "- **Attention Question**: %s\n", detail.Ticket.Attention)
		}
		if detail.Ticket.DeliveryError != "" {
			fmt.Fprintf(&b, "- **Delivery Error**: %s\n", detail.Ticket.DeliveryError)
		}
		if detail.Ticket.Note != "" {
			fmt.Fprintf(&b, "- **Routing Note**: %s\n", detail.Ticket.Note)
		}
		fmt.Fprintf(&b, "- **Requester**: %s (source: `%s`)\n", detail.Ticket.Mandate.Requester.Label(), detail.Ticket.Mandate.Source)
		if len(detail.Ticket.Mandate.MatchedRules) > 0 {
			fmt.Fprintf(&b, "- **Matched Rules**: %s\n", strings.Join(detail.Ticket.Mandate.MatchedRules, ", "))
		}
		fmt.Fprintf(&b, "- **Mandate**: strength=`%s`, basis=`%s`, policy_version=`%s`\n", detail.Ticket.Mandate.Strength, detail.Ticket.Mandate.Basis, detail.Ticket.Mandate.PolicyVersion)
		if detail.ActiveAssignment != nil {
			fmt.Fprintf(&b, "- **Active Assignment**: `%s` (status: `%s`)\n", detail.ActiveAssignment.ID, detail.ActiveAssignment.Status)
		}
		if detail.Ticket.LastReport != nil {
			fmt.Fprintf(&b, "- **Latest Report** [%s]: %s\n", detail.Ticket.LastReport.State, detail.Ticket.LastReport.Summary)
			if detail.Ticket.LastReport.Question != "" {
				fmt.Fprintf(&b, "  *Report Question*: %s\n", detail.Ticket.LastReport.Question)
			}
		}
		if len(detail.Inputs) > 0 {
			fmt.Fprintf(&b, "- **Inputs** (%d):\n", len(detail.Inputs))
			for _, in := range detail.Inputs {
				kindStr := string(in.Kind)
				if in.Owned {
					kindStr += " (owned)"
				}
				preview := in.Text
				if len([]rune(preview)) > 60 {
					preview = string([]rune(preview)[:60]) + "..."
				}
				fmt.Fprintf(&b, "  - [%s, %s] %s\n", kindStr, in.Status, preview)
			}
		}
		return CommandResult{Handled: true, Output: strings.TrimSpace(b.String())}, nil

	case "/stop":
		if e.saEngine == nil {
			return CommandResult{Handled: true, Output: "SuperAgent mode is not active."}, nil
		}
		fields := strings.Fields(args)
		if len(fields) == 0 {
			return CommandResult{Handled: true, Output: "Usage: `/stop <id|name> [reason]`"}, nil
		}
		query := fields[0]
		reason := "stopped by user via command"
		if len(fields) > 1 {
			reason = strings.Join(fields[1:], " ")
		}
		t, err := e.saEngine.FindTicketByIDOrName(ctx, query)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if t == nil {
			return CommandResult{Handled: true, Output: fmt.Sprintf("Ticket `%s` not found.", query)}, nil
		}
		if err := e.saEngine.StopTicket(ctx, t.ID, reason); err != nil {
			return CommandResult{Handled: true}, err
		}
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Ticket `%s` (`%s`) stopped. Mandate withdrawn.", t.Name, t.ID),
		}, nil

	case "/retry":
		if e.saEngine == nil {
			return CommandResult{Handled: true, Output: "SuperAgent mode is not active."}, nil
		}
		query := strings.TrimSpace(args)
		if query == "" {
			return CommandResult{Handled: true, Output: "Usage: `/retry <id|name>`"}, nil
		}
		t, err := e.saEngine.FindTicketByIDOrName(ctx, query)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if t == nil {
			return CommandResult{Handled: true, Output: fmt.Sprintf("Ticket `%s` not found.", query)}, nil
		}
		if err := e.saEngine.RetryTicket(ctx, t.ID); err != nil {
			return CommandResult{Handled: true}, err
		}
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Ticket `%s` (`%s`) restarted for retry.", t.Name, t.ID),
		}, nil

	case "/attention":
		if e.saEngine == nil {
			return CommandResult{Handled: true, Output: "SuperAgent mode is not active."}, nil
		}
		att, err := e.saEngine.GetAttention(ctx)
		if err != nil {
			return CommandResult{Handled: true}, err
		}
		if len(att.Tickets) == 0 && len(att.UnavailableEvals) == 0 {
			return CommandResult{
				Handled: true,
				Output:  "No tickets or evaluations currently require human attention.",
			}, nil
		}
		var b strings.Builder
		total := len(att.Tickets) + len(att.UnavailableEvals)
		fmt.Fprintf(&b, "**Attention Needed (%d item(s))**\n", total)
		if len(att.Tickets) > 0 {
			b.WriteString("\n**Tickets Awaiting Human Action**:\n")
			for _, t := range att.Tickets {
				fmt.Fprintf(&b, "- `%s` [%s]: %s (ID: `%s`)\n", t.Name, t.Stage, t.Title, t.TicketID)
				if t.Attention != "" {
					fmt.Fprintf(&b, "  *Question*: %s\n", t.Attention)
				}
				if t.DeliveryError != "" {
					fmt.Fprintf(&b, "  *Delivery Error*: %s\n", t.DeliveryError)
				}
				if t.LastReport != nil && t.LastReport.Summary != "" {
					fmt.Fprintf(&b, "  *Report*: %s\n", t.LastReport.Summary)
				}
			}
		}
		if len(att.UnavailableEvals) > 0 {
			b.WriteString("\n**Unavailable Evaluations** (use `/dismiss <id>` to dismiss):\n")
			for _, d := range att.UnavailableEvals {
				fmt.Fprintf(&b, "- Decision `%s` (thread `%s`): %s\n", d.ID, d.Thread, d.Reason)
			}
		}
		return CommandResult{Handled: true, Output: strings.TrimSpace(b.String())}, nil

	case "/dismiss":
		if e.saEngine == nil {
			return CommandResult{Handled: true, Output: "SuperAgent mode is not active."}, nil
		}
		id := strings.TrimSpace(args)
		if id == "" {
			return CommandResult{Handled: true, Output: "Usage: `/dismiss <decision_id>`"}, nil
		}
		if err := e.saEngine.Dismiss(ctx, id); err != nil {
			return CommandResult{Handled: true}, err
		}
		return CommandResult{
			Handled: true,
			Output:  fmt.Sprintf("Decision `%s` dismissed.", id),
		}, nil

	default:
		return CommandResult{}, nil
	}
}
