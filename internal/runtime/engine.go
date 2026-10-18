package runtime

import (
	"context"
	"fmt"
	"iter"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/mcp"
	"github.com/gosolu/clapbot/internal/memory"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/clapbot/internal/tools/channel"
	"github.com/gosolu/clapbot/internal/tools/cron"
	"github.com/gosolu/clapbot/internal/tools/web"
	builtin_skills "github.com/gosolu/clapbot/skills/builtin"
	"github.com/gosolu/react"
	"github.com/gosolu/react/tools/builtin"
)

// TurnRequest describes an inbound user turn from any channel (CLI, Telegram, Feishu, Cron, Gateway).
type TurnRequest struct {
	SessionID        string
	Channel          string
	ChatID           string
	Input            string
	Parts            []react.Part
	DefaultWorkspace string
}

// Engine is the central Clapbot runtime wrapping gosolu/react.Agent, SQLiteStore, Memory, Scheduler, and Bus.
type Engine struct {
	cfg        *config.Config
	store      *store.SQLiteStore
	router     *provider.Router
	memory     *memory.Manager
	scheduler  *cron.Scheduler
	bus        *Bus
	agent      *react.Agent
	summarizer *react.ContextSummarizer

	turnMetaMu sync.RWMutex
	turnMeta   map[string]TurnRequest
}

// NewEngine constructs and initializes the Clapbot runtime Engine.
func NewEngine(cfg *config.Config, st *store.SQLiteStore, router *provider.Router) (*Engine, error) {
	if router == nil {
		var err error
		router, err = provider.NewRouter(cfg)
		if err != nil {
			return nil, err
		}
	}

	bus := NewBus()
	mem := memory.NewManager(cfg.HomeDir, st)

	eng := &Engine{
		cfg:      cfg,
		store:    st,
		router:   router,
		memory:   mem,
		bus:      bus,
		turnMeta: map[string]TurnRequest{},
	}

	eng.scheduler = cron.NewScheduler(st, eng.onScheduledTrigger)

	sumModel := cfg.DefaultProfile
	if router.HasProfile("fast") {
		sumModel = "fast"
	}
	eng.summarizer = &react.ContextSummarizer{
		Provider:    router,
		Model:       sumModel,
		RecentRatio: 0.5,
		MaxTokens:   1024,
	}

	policy := react.AllowAllPermissionPolicy()
	if strings.EqualFold(cfg.Permissions.Mode, "rules") || len(cfg.Permissions.Deny) > 0 {
		policy = react.PermissionRules{
			Allow:    cfg.Permissions.Allow,
			Ask:      cfg.Permissions.Ask,
			Deny:     cfg.Permissions.Deny,
			Fallback: react.AllowAllPermissionPolicy(),
		}
	}

	var baseTools []react.Tool
	baseTools = append(baseTools, builtin.NewFilesystem(cfg.Workspace)...)
	baseTools = append(baseTools, builtin.Shell(builtin.WithWorkDir(cfg.Workspace)))
	baseTools = append(baseTools, mem.Tools()...)
	baseTools = append(baseTools, web.Tools(cfg.Web, nil)...)
	baseTools = append(baseTools, eng.scheduler.Tools()...)
	baseTools = append(baseTools, channel.Tools(bus)...)

	// Build configured sub-agents using gosolu/react's AsTool primitive.
	for _, saCfg := range cfg.Subagents {
		if strings.TrimSpace(saCfg.Name) == "" {
			continue
		}
		saProfile := saCfg.Profile
		if saProfile == "" {
			saProfile = cfg.DefaultProfile
		}
		saPrompt := saCfg.SystemPrompt
		if saPrompt == "" {
			saPrompt = fmt.Sprintf("You are %s, a specialized sub-agent of Clapbot. %s", saCfg.Name, saCfg.Description)
		}
		subTools := append([]react.Tool{}, builtin.NewFilesystem(cfg.Workspace)...)
		subTools = append(subTools, builtin.Shell(builtin.WithWorkDir(cfg.Workspace)))
		subTools = append(subTools, web.Tools(cfg.Web, nil)...)
		subTools = append(subTools, mem.Tools()...)

		subAgent, err := react.New(
			react.WithName(saCfg.Name),
			react.WithProvider(router),
			react.WithModel(saProfile),
			react.WithSystemPrompt(saPrompt),
			react.WithTools(subTools...),
			react.WithPermission(policy, react.AlwaysApprove()),
			react.WithMaxTurns(15),
		)
		if err == nil {
			baseTools = append(baseTools, subAgent.AsTool("subagent_"+saCfg.Name, saCfg.Description))
		}
	}

	skillSources := []react.SkillSource{
		builtin_skills.SkillSource(),
		react.SkillDir(cfg.SkillsDir()),
		react.SkillDir(filepath.Join(cfg.Workspace, ".clapbot", "skills")),
	}
	for _, dir := range react.StandardSkillDirs(cfg.Workspace) {
		skillSources = append(skillSources, react.SkillDir(dir))
	}

	hooks := react.Hooks{
		OnRunStart: func(ctx context.Context, rc *react.RunContext) error {
			meta := eng.getTurnMeta(rc.SessionID)
			ws := meta.DefaultWorkspace
			if sess, _ := eng.store.GetSession(ctx, rc.SessionID); sess != nil && sess.WorkspaceDir != "" {
				ws = sess.WorkspaceDir
			}
			if ws == "" {
				ws = eng.cfg.Workspace
			}
			rc.Set(cron.ContextKeyWorkspace, ws)
			rc.Set(cron.ContextKeyChannel, meta.Channel)
			rc.Set(cron.ContextKeyChatID, meta.ChatID)
			return nil
		},
		BeforeModel: func(ctx context.Context, rc *react.RunContext, req *react.Request) error {
			ws, _ := rc.Get(cron.ContextKeyWorkspace)
			ch, _ := rc.Get(cron.ContextKeyChannel)
			wsStr, _ := ws.(string)
			chStr, _ := ch.(string)
			if wsStr == "" {
				wsStr = eng.cfg.Workspace
			}
			if chStr == "" {
				chStr = "cli"
			}
			dyn := eng.memory.ComposeSystemContext(wsStr, chStr, rc.SessionID)
			if strings.TrimSpace(req.System) != "" {
				req.System = dyn + "\n\n---\n\n" + req.System
			} else {
				req.System = dyn
			}
			return nil
		},
	}

	agentOpts := []react.Option{
		react.WithName("clapbot"),
		react.WithProvider(router),
		react.WithModel(cfg.DefaultProfile),
		react.WithStore(st),
		react.WithContextManager(eng.summarizer, cfg.ContextBudget),
		react.WithPersistCompaction(),
		react.WithPermission(policy, react.AlwaysApprove()),
		react.WithTools(baseTools...),
		react.WithSkillSources(skillSources...),
		react.WithHooks(hooks),
		react.WithMaxTurns(25),
	}
	if len(cfg.MCPServers) > 0 {
		agentOpts = append(agentOpts, react.WithPlugins(mcp.NewPlugin(cfg.MCPServers)))
	}

	ag, err := react.New(agentOpts...)
	if err != nil {
		return nil, fmt.Errorf("create react agent: %w", err)
	}
	eng.agent = ag
	return eng, nil
}

// Config returns the active Config.
func (e *Engine) Config() *config.Config { return e.cfg }

// Store returns the SQLiteStore.
func (e *Engine) Store() *store.SQLiteStore { return e.store }

// Router returns the model profile Router.
func (e *Engine) Router() *provider.Router { return e.router }

// Memory returns the Markdown + FTS5 memory Manager.
func (e *Engine) Memory() *memory.Manager { return e.memory }

// Scheduler returns the cron/timer Scheduler.
func (e *Engine) Scheduler() *cron.Scheduler { return e.scheduler }

// Bus returns the message and event Bus.
func (e *Engine) Bus() *Bus { return e.bus }

// Agent returns the underlying gosolu/react Agent.
func (e *Engine) Agent() *react.Agent { return e.agent }

// Start starts the background scheduler and all registered channel adapters.
func (e *Engine) Start(ctx context.Context) error {
	if err := e.scheduler.Start(ctx); err != nil {
		return err
	}
	for _, ch := range e.bus.Channels() {
		if err := ch.Start(ctx); err != nil {
			return fmt.Errorf("start channel %s: %w", ch.Name(), err)
		}
	}
	return nil
}

// Close shuts down channels, scheduler, agent plugins, and store.
func (e *Engine) Close() error {
	for _, ch := range e.bus.Channels() {
		_ = ch.Stop()
	}
	if e.scheduler != nil {
		e.scheduler.Stop()
	}
	if e.agent != nil {
		_ = e.agent.Close()
	}
	return nil
}

func (e *Engine) setTurnMeta(sessionID string, req TurnRequest) {
	e.turnMetaMu.Lock()
	defer e.turnMetaMu.Unlock()
	e.turnMeta[sessionID] = req
}

func (e *Engine) getTurnMeta(sessionID string) TurnRequest {
	e.turnMetaMu.RLock()
	defer e.turnMetaMu.RUnlock()
	return e.turnMeta[sessionID]
}

// ResolveChannelSession looks up or creates the bound sessionID for a (channel, chatID) pair.
func (e *Engine) ResolveChannelSession(ctx context.Context, channelName, chatID string) (string, error) {
	sid, err := e.store.GetBoundSession(ctx, channelName, chatID)
	if err != nil {
		return "", err
	}
	if sid == "" {
		sid = fmt.Sprintf("%s_%s", channelName, chatID)
		if _, err := e.store.EnsureSession(ctx, sid, e.cfg.Workspace, e.cfg.DefaultProfile); err != nil {
			return "", err
		}
		if err := e.store.SetBoundSession(ctx, channelName, chatID, sid); err != nil {
			return "", err
		}
	}
	return sid, nil
}

func (e *Engine) ensureSessionForTurn(ctx context.Context, req TurnRequest) (*store.Session, error) {
	sid := req.SessionID
	if sid == "" && req.Channel != "" && req.ChatID != "" {
		var err error
		sid, err = e.ResolveChannelSession(ctx, req.Channel, req.ChatID)
		if err != nil {
			return nil, err
		}
	}
	if sid == "" {
		sid = "default"
	}
	ws := req.DefaultWorkspace
	if ws == "" {
		ws = e.cfg.Workspace
	}
	return e.store.EnsureSession(ctx, sid, ws, e.cfg.DefaultProfile)
}

// StreamTurn executes a turn (or slash command) and yields react.Events.
func (e *Engine) StreamTurn(ctx context.Context, req TurnRequest) iter.Seq2[react.Event, error] {
	return func(yield func(react.Event, error) bool) {
		sess, err := e.ensureSessionForTurn(ctx, req)
		if err != nil {
			yield(react.Event{Type: react.EventError, Err: err}, err)
			return
		}
		req.SessionID = sess.ID
		e.setTurnMeta(sess.ID, req)

		// Build workspace-scoped file & shell tools for this session's bound workspace.
		wsTools := append([]react.Tool{}, builtin.NewFilesystem(sess.WorkspaceDir)...)
		wsTools = append(wsTools, builtin.Shell(builtin.WithWorkDir(sess.WorkspaceDir)))

		runOpts := []react.RunOption{
			react.WithRunModel(sess.Profile),
			react.WithRunTools(wsTools...),
		}
		if len(req.Parts) > 0 {
			runOpts = append(runOpts, react.WithInputParts(req.Parts...))
		}

		for ev, err := range e.agent.Stream(ctx, sess.ID, req.Input, runOpts...) {
			e.bus.Publish(TurnEvent{
				SessionID: sess.ID,
				Channel:   req.Channel,
				ChatID:    req.ChatID,
				Event:     ev,
			})
			if !yield(ev, err) {
				return
			}
		}
	}
}

// RunTurn executes a non-streaming turn (or slash command) to completion.
func (e *Engine) RunTurn(ctx context.Context, req TurnRequest) (*react.Result, CommandResult, error) {
	sess, err := e.ensureSessionForTurn(ctx, req)
	if err != nil {
		return nil, CommandResult{}, err
	}
	req.SessionID = sess.ID

	cmdRes, err := e.HandleCommand(ctx, req)
	if err != nil || cmdRes.Handled {
		return nil, cmdRes, err
	}

	e.setTurnMeta(sess.ID, req)
	wsTools := append([]react.Tool{}, builtin.NewFilesystem(sess.WorkspaceDir)...)
	wsTools = append(wsTools, builtin.Shell(builtin.WithWorkDir(sess.WorkspaceDir)))

	runOpts := []react.RunOption{
		react.WithRunModel(sess.Profile),
		react.WithRunTools(wsTools...),
		react.WithEventHandler(func(ev react.Event) {
			e.bus.Publish(TurnEvent{
				SessionID: sess.ID,
				Channel:   req.Channel,
				ChatID:    req.ChatID,
				Event:     ev,
			})
		}),
	}
	if len(req.Parts) > 0 {
		runOpts = append(runOpts, react.WithInputParts(req.Parts...))
	}

	res, err := e.agent.Run(ctx, sess.ID, req.Input, runOpts...)
	return res, CommandResult{}, err
}

// CompactSession forces an immediate LLM-based compaction of a session's message history.
func (e *Engine) CompactSession(ctx context.Context, sessionID string) (bool, int, int, error) {
	msgs, err := e.store.Load(ctx, sessionID)
	if err != nil {
		return false, 0, 0, err
	}
	before := len(msgs)
	if before <= 4 {
		return false, before, before, nil
	}
	// Force compaction by passing a tight budget to ContextSummarizer.Fit
	compacted, err := e.summarizer.Fit(ctx, "", msgs, 1)
	if err != nil {
		return false, before, before, err
	}
	if len(compacted) >= before {
		return false, before, len(compacted), nil
	}
	if err := e.store.Replace(ctx, sessionID, compacted); err != nil {
		return false, before, before, err
	}
	return true, before, len(compacted), nil
}

func (e *Engine) onScheduledTrigger(ctx context.Context, sch store.Schedule) {
	sid := sch.SessionID
	if sid == "" && sch.Channel != "" && sch.ChatID != "" {
		sid, _ = e.ResolveChannelSession(ctx, sch.Channel, sch.ChatID)
	}
	if sid == "" {
		sid = "schedule_" + sch.ID
	}
	prompt := fmt.Sprintf("[Scheduled Task %q (%s) triggered]\n%s", sch.Name, sch.ID, sch.Prompt)
	res, _, err := e.RunTurn(ctx, TurnRequest{
		SessionID: sid,
		Channel:   sch.Channel,
		ChatID:    sch.ChatID,
		Input:     prompt,
	})
	if err != nil {
		log.Printf("[scheduler] schedule %s failed: %v", sch.ID, err)
		return
	}
	if res != nil && strings.TrimSpace(res.Output) != "" && sch.Channel != "" && sch.ChatID != "" {
		_ = e.bus.SendText(ctx, sch.Channel, sch.ChatID, fmt.Sprintf("⏰ **%s**\n\n%s", sch.Name, res.Output))
	}
}
