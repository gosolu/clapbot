package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gosolu/clapbot/internal/channels/feishu"
	"github.com/gosolu/clapbot/internal/channels/telegram"
	"github.com/gosolu/clapbot/internal/cli"
	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/gateway"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/service"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/httpr"
	"github.com/gosolu/xslog"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "clapbot: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	http.DefaultTransport = httpr.NewRoundTripper()
	setupLogger("info", "text", true, "")

	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printUsage()
		return nil
	}

	subcmd := "chat"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		subcmd = args[0]
		args = args[1:]
	}

	switch subcmd {
	case "chat", "agent":
		return runChat(args)
	case "run":
		return runOneShot(args)
	case "serve", "gateway":
		return runServe(args)
	case "onboard", "init", "quickstart":
		return runOnboard(args)
	case "service":
		return runService(args)
	case "status":
		return runStatus(args)
	default:
		return fmt.Errorf("unknown command %q (run `clapbot help`)", subcmd)
	}
}

func printUsage() {
	fmt.Println(`👏 Clapbot — Personal AI Assistant & Multi-Channel Agent Gateway (powered by gosolu/react)

Usage:
  clapbot [chat] [flags]            Interactive terminal REPL (attaches to Gateway if running)
  clapbot run -m "<prompt>" [flags] Execute a single prompt and print the response
  clapbot serve [flags]             Start the Gateway daemon, Scheduler, Telegram, and Feishu channels
  clapbot onboard [flags]           Interactive setup wizard for providers and chat channels
  clapbot status [flags]            Show configuration, gateway, and service status
  clapbot service <action>          Manage background service: install | uninstall | start | stop | status | logs

Common Flags:
  --home <dir>                      Clapbot home directory (default ~/.clapbot or $CLAPBOT_HOME)
  --session <id>                    Resume a specific session ID
  --workspace <dir>                 Workspace directory for file and shell tools`)
}

func runChat(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	homeDir := fs.String("home", "", "Clapbot home directory")
	sessionID := fs.String("session", "", "Session ID to resume")
	workspace := fs.String("workspace", "", "Workspace directory")
	prompt := fs.String("m", "", "Run a single message and exit")
	direct := fs.Bool("direct", false, "Force in-process execution without attaching to Gateway")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *prompt != "" {
		return executeSinglePrompt(*homeDir, *sessionID, *workspace, *prompt)
	}

	cfg, err := config.Load(*homeDir)
	if err != nil {
		return err
	}

	ctx := context.Background()
	if !*direct {
		gwClient := gateway.NewClient(cfg.GatewayAddr, cfg.GatewayToken)
		if gwClient.Ping(ctx) {
			return cli.RunREPL(ctx, cli.REPLOptions{
				SessionID: *sessionID,
				Workspace: *workspace,
				Gateway:   gwClient,
			})
		}
	}

	eng, cleanup, err := initEngine(cfg, false)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := eng.Start(ctx); err != nil {
		return err
	}
	return cli.RunREPL(ctx, cli.REPLOptions{
		SessionID: *sessionID,
		Workspace: *workspace,
		Engine:    eng,
	})
}

func runOneShot(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	homeDir := fs.String("home", "", "Clapbot home directory")
	sessionID := fs.String("session", "", "Session ID")
	workspace := fs.String("workspace", "", "Workspace directory")
	prompt := fs.String("m", "", "Prompt message")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	msg := strings.TrimSpace(*prompt)
	if msg == "" && fs.NArg() > 0 {
		msg = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if msg == "" {
		return fmt.Errorf("prompt is required (use -m \"...\" or positional args)")
	}
	return executeSinglePrompt(*homeDir, *sessionID, *workspace, msg)
}

func executeSinglePrompt(homeDir, sessionID, workspace, prompt string) error {
	cfg, err := config.Load(homeDir)
	if err != nil {
		return err
	}
	eng, cleanup, err := initEngine(cfg, false)
	if err != nil {
		return err
	}
	defer cleanup()

	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	res, cmdRes, err := eng.RunTurn(context.Background(), runtime.TurnRequest{
		SessionID:        sessionID,
		Channel:          "cli",
		ChatID:           "oneshot",
		Input:            prompt,
		DefaultWorkspace: workspace,
	})
	if err != nil {
		return err
	}
	if cmdRes.Handled {
		fmt.Println(cmdRes.Output)
		return nil
	}
	fmt.Println(res.Output)
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	homeDir := fs.String("home", "", "Clapbot home directory")
	addr := fs.String("addr", "", "Gateway listen address override (default from config.toml)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := config.Load(*homeDir)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.GatewayAddr = *addr
	}

	eng, cleanup, err := initEngine(cfg, true)
	if err != nil {
		return err
	}
	defer cleanup()

	// Register Telegram and Feishu channels
	if cfg.Channels.Telegram.Enabled {
		eng.Bus().RegisterChannel(telegram.New(cfg.Channels.Telegram, cfg.UploadsDir(), eng))
	}
	if cfg.Channels.Feishu.Enabled {
		eng.Bus().RegisterChannel(feishu.New(cfg.Channels.Feishu, cfg.UploadsDir(), eng))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := eng.Start(ctx); err != nil {
		return err
	}

	gw := gateway.NewServer(cfg.GatewayAddr, eng)
	if err := gw.Start(); err != nil {
		return err
	}
	slog.InfoContext(ctx, "gateway listening", "url", "http://"+gw.Addr(), "home", cfg.HomeDir)

	<-ctx.Done()
	slog.Info("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = gw.Shutdown(shutCtx)
	return nil
}

func runOnboard(args []string) error {
	fs := flag.NewFlagSet("onboard", flag.ContinueOnError)
	homeDir := fs.String("home", "", "Clapbot home directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return cli.RunOnboarding(*homeDir)
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	homeDir := fs.String("home", "", "Clapbot home directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := config.Load(*homeDir)
	if err != nil {
		return err
	}
	gw := gateway.NewClient(cfg.GatewayAddr, cfg.GatewayToken)
	alive := gw.Ping(context.Background())
	svc := &service.Manager{HomeDir: cfg.HomeDir, LogFile: cfg.LogFile()}

	fmt.Printf("Clapbot Home:      %s\n", cfg.HomeDir)
	fmt.Printf("Config File:       %s\n", cfg.ConfigPath())
	fmt.Printf("Default Profile:   %s\n", cfg.DefaultProfile)
	fmt.Printf("Default Workspace: %s\n", cfg.Workspace)
	fmt.Printf("Gateway Address:   %s (reachable: %v)\n", cfg.GatewayAddr, alive)
	fmt.Printf("Telegram Enabled:  %v\n", cfg.Channels.Telegram.Enabled)
	fmt.Printf("Feishu Enabled:    %v\n", cfg.Channels.Feishu.Enabled)
	fmt.Printf("OS Service:        %s\n", svc.Status())
	return nil
}

func runService(args []string) error {
	action := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	homeDir := fs.String("home", "", "Clapbot home directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := config.Load(*homeDir)
	if err != nil {
		return err
	}
	svc := &service.Manager{
		HomeDir: cfg.HomeDir,
		LogFile: cfg.LogFile(),
	}
	switch action {
	case "install":
		msg, err := svc.Install()
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	case "uninstall":
		if err := svc.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Uninstalled Clapbot service.")
		return nil
	case "start":
		if err := svc.Start(); err != nil {
			return err
		}
		fmt.Println("Started Clapbot background service.")
		return nil
	case "stop":
		if err := svc.Stop(); err != nil {
			return err
		}
		fmt.Println("Stopped Clapbot background service.")
		return nil
	case "status":
		fmt.Println(svc.Status())
		return nil
	case "logs":
		b, err := os.ReadFile(cfg.LogFile())
		if err != nil {
			return err
		}
		lines := strings.Split(string(b), "\n")
		if len(lines) > 100 {
			lines = lines[len(lines)-100:]
		}
		fmt.Println(strings.Join(lines, "\n"))
		return nil
	default:
		return fmt.Errorf("unknown service action %q (expected install|uninstall|start|stop|status|logs)", action)
	}
}

func initEngine(cfg *config.Config, enableFile bool) (*runtime.Engine, func(), error) {
	logFile := ""
	if enableFile || cfg.Logging.File != "" {
		logFile = cfg.LogFile()
	}
	logCloser := setupLogger(cfg.Logging.Level, cfg.Logging.Format, cfg.Logging.Source || cfg.Logging.Level == "", logFile)

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		if logCloser != nil {
			_ = logCloser.Close()
		}
		return nil, nil, err
	}
	router, err := provider.NewRouter(cfg)
	if err != nil {
		_ = st.Close()
		if logCloser != nil {
			_ = logCloser.Close()
		}
		return nil, nil, err
	}
	eng, err := runtime.NewEngine(cfg, st, router)
	if err != nil {
		_ = st.Close()
		if logCloser != nil {
			_ = logCloser.Close()
		}
		return nil, nil, err
	}
	cleanup := func() {
		_ = eng.Close()
		_ = st.Close()
		if logCloser != nil {
			_ = logCloser.Close()
		}
	}
	return eng, cleanup, nil
}

func setupLogger(levelStr, format string, addSource bool, logPath string) io.Closer {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var w io.Writer = os.Stderr
	var closer io.Closer
	if logPath != "" {
		rot := xslog.NewRotateWriter(logPath,
			xslog.WithMaxSize(100),
			xslog.WithMaxBackups(5),
			xslog.WithCompress(true),
		)
		closer = rot
		w = io.MultiWriter(os.Stderr, rot)
	}

	hOpts := &slog.HandlerOptions{
		AddSource: addSource,
		Level:     level,
		ReplaceAttr: xslog.ReplaceAttr(
			xslog.AttrSimpleSource,
			xslog.AttrLowerLevel,
		),
	}

	var base slog.Handler
	if strings.EqualFold(format, "json") {
		base = slog.NewJSONHandler(w, hOpts)
	} else {
		base = slog.NewTextHandler(w, hOpts)
	}

	slog.SetDefault(slog.New(xslog.UseContext(base)))
	return closer
}
