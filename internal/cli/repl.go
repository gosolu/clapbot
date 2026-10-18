package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/gosolu/clapbot/internal/gateway"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/react"
)

const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
	ansiCyan  = "\033[36m"
	ansiGreen = "\033[32m"
	ansiRed   = "\033[31m"
)

// REPLOptions configures an interactive CLI session.
type REPLOptions struct {
	SessionID string
	Workspace string
	Engine    *runtime.Engine // Used when running embedded in-process
	Gateway   *gateway.Client // Used when attaching to a running local Gateway daemon
}

// RunREPL runs the native-scrollback interactive terminal chat loop.
func RunREPL(ctx context.Context, opts REPLOptions) error {
	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = runtime.NewSessionID("cli")
	}
	workspace := opts.Workspace
	if workspace == "" {
		if wd, err := os.Getwd(); err == nil {
			workspace = wd
		}
	}

	mode := "in-process"
	if opts.Gateway != nil {
		mode = "gateway-attached"
	}

	fmt.Printf("%s%s🦀 Clapbot%s %s(%s | session: %s | workspace: %s)%s\n",
		ansiBold, ansiCyan, ansiReset, ansiDim, mode, sessionID, workspace, ansiReset)
	fmt.Printf("%sType /help for commands, /sessions to switch conversations, /model to switch models, or /exit to quit.%s\n\n",
		ansiDim, ansiReset)

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s%syou ›%s ", ansiBold, ansiGreen, ansiReset)
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println()
				return nil
			}
			return err
		}
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		// Handle interactive TUI pickers for /sessions and /model when invoked with no arguments
		if input == "/sessions" && IsTTY() {
			picked, err := handleInteractiveSessionPick(ctx, opts, sessionID)
			if err == nil && picked != "" {
				if picked == "__new__" {
					input = "/new"
				} else {
					input = "/sessions " + picked
				}
			}
		} else if (input == "/model" || input == "/models") && IsTTY() {
			picked, err := handleInteractiveModelPick(ctx, opts, sessionID)
			if err == nil && picked != "" {
				input = "/model " + picked
			}
		}

		req := runtime.TurnRequest{
			SessionID:        sessionID,
			Channel:          "cli",
			ChatID:           "local",
			Input:            input,
			DefaultWorkspace: workspace,
		}

		// Run turn with Ctrl+C cancellation per turn
		turnCtx, stopSig := signal.NotifyContext(ctx, os.Interrupt)
		newSID, exit, err := executeREPLTurn(turnCtx, opts, req)
		stopSig()

		if err != nil {
			fmt.Printf("\n%s[error] %v%s\n\n", ansiRed, err, ansiReset)
		}
		if newSID != "" {
			sessionID = newSID
		}
		if exit {
			return nil
		}
	}
}

func executeREPLTurn(ctx context.Context, opts REPLOptions, req runtime.TurnRequest) (string, bool, error) {
	if opts.Gateway != nil {
		if strings.HasPrefix(req.Input, "/") {
			cmdRes, err := opts.Gateway.Command(ctx, req)
			if err != nil {
				return "", false, err
			}
			if cmdRes.Handled {
				if cmdRes.Output != "" {
					fmt.Printf("\n%s\n\n", cmdRes.Output)
				}
				return cmdRes.NewSessionID, cmdRes.Exit || cmdRes.Detach, nil
			}
		}
		return streamGatewayTurn(ctx, opts.Gateway, req)
	}

	if strings.HasPrefix(req.Input, "/") {
		cmdRes, err := opts.Engine.HandleCommand(ctx, req)
		if err != nil {
			return "", false, err
		}
		if cmdRes.Handled {
			if cmdRes.Output != "" {
				fmt.Printf("\n%s\n\n", cmdRes.Output)
			}
			return cmdRes.NewSessionID, cmdRes.Exit || cmdRes.Detach, nil
		}
	}
	return streamEngineTurn(ctx, opts.Engine, req)
}

func streamEngineTurn(ctx context.Context, eng *runtime.Engine, req runtime.TurnRequest) (string, bool, error) {
	var inReasoning, inText bool
	for ev, err := range eng.StreamTurn(ctx, req) {
		if err != nil {
			if inReasoning || inText {
				fmt.Println()
			}
			return "", false, err
		}
		renderEvent(ev.Type, ev.Agent, ev.Depth, ev.Text,
			func() string {
				if ev.ToolCall != nil {
					return ev.ToolCall.Name
				}
				return ""
			}(),
			func() string {
				if ev.ToolCall != nil {
					return string(ev.ToolCall.Arguments)
				}
				return ""
			}(),
			ev.ToolResult != nil && ev.ToolResult.IsError,
			&inReasoning, &inText,
		)
	}
	if inReasoning || inText {
		fmt.Print(ansiReset + "\n")
	}
	fmt.Println()
	return "", false, nil
}

func streamGatewayTurn(ctx context.Context, gw *gateway.Client, req runtime.TurnRequest) (string, bool, error) {
	var inReasoning, inText bool
	for frame, err := range gw.StreamTurn(ctx, req) {
		if err != nil {
			if inReasoning || inText {
				fmt.Println()
			}
			return "", false, err
		}
		if frame.Type == "command" && frame.Command != nil {
			if frame.Command.Output != "" {
				fmt.Printf("\n%s\n\n", frame.Command.Output)
			}
			return frame.Command.NewSessionID, frame.Command.Exit || frame.Command.Detach, nil
		}
		renderEvent(frame.EventType, frame.Agent, frame.Depth, frame.Text,
			frame.ToolName, frame.ToolArgs, frame.ToolIsError,
			&inReasoning, &inText)
	}
	if inReasoning || inText {
		fmt.Print(ansiReset + "\n")
	}
	fmt.Println()
	return "", false, nil
}

func renderEvent(
	evType react.EventType,
	agentName string,
	depth int,
	text string,
	toolName string,
	toolArgs string,
	toolIsErr bool,
	inReasoning *bool,
	inText *bool,
) {
	switch evType {
	case react.EventReasoningDelta:
		if depth > 0 {
			return
		}
		if !*inReasoning {
			fmt.Printf("\n%s💭 ", ansiDim)
			*inReasoning = true
		}
		fmt.Print(text)

	case react.EventTextDelta:
		if depth > 0 {
			return
		}
		if *inReasoning {
			fmt.Print(ansiReset + "\n\n")
			*inReasoning = false
		}
		if !*inText {
			fmt.Print("\n")
			*inText = true
		}
		fmt.Print(text)

	case react.EventToolCallReady:
		if *inReasoning || *inText {
			fmt.Print(ansiReset + "\n")
			*inReasoning = false
			*inText = false
		}
		prefix := "🔧"
		if depth > 0 {
			prefix = fmt.Sprintf("  ↳ [%s] 🔧", agentName)
		}
		argsPreview := compactArgs(toolArgs, 90)
		fmt.Printf("%s%s %s(%s)%s\n", ansiCyan, prefix, toolName, argsPreview, ansiReset)

	case react.EventToolResult:
		if toolIsErr {
			fmt.Printf("%s  ✗ %s returned error%s\n", ansiRed, toolName, ansiReset)
		}
	}
}

func compactArgs(s string, maxLen int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func handleInteractiveSessionPick(ctx context.Context, opts REPLOptions, currentID string) (string, error) {
	if opts.Gateway != nil {
		list, err := opts.Gateway.ListSessions(ctx)
		if err != nil {
			return "", err
		}
		return PickSession(list, currentID)
	}
	if opts.Engine != nil {
		list, err := opts.Engine.Store().ListSessions(ctx, 30)
		if err != nil {
			return "", err
		}
		return PickSession(list, currentID)
	}
	return "", nil
}

func handleInteractiveModelPick(ctx context.Context, opts REPLOptions, currentSID string) (string, error) {
	if opts.Gateway != nil {
		items, defProf, err := opts.Gateway.ListProfiles(ctx)
		if err != nil {
			return "", err
		}
		return PickModel(items, defProf)
	}
	if opts.Engine != nil {
		var items []gateway.ProfileItem
		for _, alias := range opts.Engine.Router().Profiles() {
			pcfg, _ := opts.Engine.Router().Describe(alias)
			items = append(items, gateway.ProfileItem{
				Alias:    alias,
				Provider: pcfg.Provider,
				Model:    pcfg.Model,
			})
		}
		curProf := opts.Engine.Router().DefaultProfile()
		if sess, _ := opts.Engine.Store().GetSession(ctx, currentSID); sess != nil && sess.Profile != "" {
			curProf = sess.Profile
		}
		return PickModel(items, curProf)
	}
	return "", nil
}
