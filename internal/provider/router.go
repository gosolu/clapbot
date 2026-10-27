package provider

import (
	"context"
	"fmt"
	"iter"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/react"
	rprovider "github.com/gosolu/react/provider"
	"github.com/gosolu/react/provider/anthropic"
	"github.com/gosolu/react/provider/deepseek"
	"github.com/gosolu/react/provider/gemini"
	"github.com/gosolu/react/provider/mock"
	"github.com/gosolu/react/provider/openai"
	"github.com/gosolu/react/provider/opencode"
)

type profileEntry struct {
	alias    string
	cfg      config.ProfileConfig
	provider react.Provider
}

// Router implements react.Provider and routes model calls by profile alias.
type Router struct {
	mu             sync.RWMutex
	defaultProfile string
	entries        map[string]profileEntry
}

// NewRouter constructs a multi-profile Router from configuration.
func NewRouter(cfg *config.Config) (*Router, error) {
	r := &Router{
		defaultProfile: cfg.DefaultProfile,
		entries:        make(map[string]profileEntry, len(cfg.Profiles)),
	}
	if r.defaultProfile == "" {
		r.defaultProfile = "default"
	}
	for alias, pcfg := range cfg.Profiles {
		p, err := buildProvider(pcfg)
		if err != nil {
			return nil, fmt.Errorf("provider profile %q: %w", alias, err)
		}
		r.entries[alias] = profileEntry{
			alias:    alias,
			cfg:      pcfg,
			provider: p,
		}
	}
	return r, nil
}

func buildProvider(pcfg config.ProfileConfig) (react.Provider, error) {
	apiType := rprovider.APITypeChatCompletions
	if strings.EqualFold(pcfg.WireAPI, "responses") {
		apiType = rprovider.APITypeResponses
	}
	switch strings.ToLower(strings.TrimSpace(pcfg.Provider)) {
	case "openai", "":
		return openai.New(openai.Config{
			APIKey:  pcfg.APIKey,
			BaseURL: pcfg.BaseURL,
			APIType: apiType,
		}), nil
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKey:      pcfg.APIKey,
			BaseURL:     pcfg.BaseURL,
			PromptCache: true,
		}), nil
	case "deepseek":
		return deepseek.New(deepseek.Config{
			APIKey:  pcfg.APIKey,
			BaseURL: pcfg.BaseURL,
			APIType: apiType,
		}), nil
	case "gemini", "google":
		apiKey := pcfg.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("GEMINI_API_KEY")
			if apiKey == "" {
				apiKey = os.Getenv("GOOGLE_API_KEY")
			}
		}
		gCfg := gemini.Config{
			APIKey:  apiKey,
			BaseURL: pcfg.BaseURL,
		}
		if strings.EqualFold(pcfg.WireAPI, "chat") || strings.EqualFold(pcfg.WireAPI, "chat_completions") || strings.EqualFold(pcfg.WireAPI, "openai") {
			return gemini.NewChatCompletions(gCfg), nil
		}
		return gemini.New(gCfg), nil
	case "opencode":
		return opencode.New(opencode.Config{
			APIKey:  pcfg.APIKey,
			BaseURL: pcfg.BaseURL,
			APIType: apiType,
			SessionIDFunc: func(ctx context.Context, _ *react.Request) string {
				if rc := react.RunContextFrom(ctx); rc != nil && rc.SessionID != "" {
					return rc.SessionID
				}
				return ""
			},
		}), nil
	case "mock":
		return mock.New(mock.Text("(mock response)")), nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q", pcfg.Provider)
	}
}

// RegisterProfile adds or replaces a profile entry at runtime (useful for tests or live reload).
func (r *Router) RegisterProfile(alias string, pcfg config.ProfileConfig, p react.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[string]profileEntry{}
	}
	r.entries[alias] = profileEntry{
		alias:    alias,
		cfg:      pcfg,
		provider: p,
	}
}

// Profiles returns sorted profile names and their configuration.
func (r *Router) Profiles() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.entries))
	for k := range r.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Describe returns human-readable info for a profile alias.
func (r *Router) Describe(alias string) (config.ProfileConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if alias == "" {
		alias = r.defaultProfile
	}
	e, ok := r.entries[alias]
	return e.cfg, ok
}

// HasProfile reports whether a profile alias exists.
func (r *Router) HasProfile(alias string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.entries[alias]
	return ok
}

// DefaultProfile returns the default profile alias.
func (r *Router) DefaultProfile() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultProfile
}

// Name implements react.Provider.
func (r *Router) Name() string {
	return "clapbot-router"
}

func (r *Router) resolve(req *react.Request) (react.Provider, *react.Request, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	target := strings.TrimSpace(req.Model)
	if target == "" {
		target = r.defaultProfile
	}

	if entry, ok := r.entries[target]; ok {
		cloned := *req
		cloned.Model = entry.cfg.Model
		return entry.provider, &cloned, nil
	}

	// Fallback to default profile's provider with req.Model preserved if set.
	if entry, ok := r.entries[r.defaultProfile]; ok {
		cloned := *req
		if cloned.Model == "" {
			cloned.Model = entry.cfg.Model
		}
		return entry.provider, &cloned, nil
	}

	// Pick any available profile if default is missing.
	for _, entry := range r.entries {
		cloned := *req
		if cloned.Model == "" {
			cloned.Model = entry.cfg.Model
		}
		return entry.provider, &cloned, nil
	}
	return nil, nil, fmt.Errorf("no model profiles configured")
}

// Generate implements react.Provider.
func (r *Router) Generate(ctx context.Context, req *react.Request) (*react.Response, error) {
	p, routedReq, err := r.resolve(req)
	if err != nil {
		return nil, err
	}
	return p.Generate(ctx, routedReq)
}

// Stream implements react.Provider.
func (r *Router) Stream(ctx context.Context, req *react.Request) iter.Seq2[react.StreamEvent, error] {
	p, routedReq, err := r.resolve(req)
	if err != nil {
		return func(yield func(react.StreamEvent, error) bool) {
			yield(react.StreamEvent{}, err)
		}
	}
	return p.Stream(ctx, routedReq)
}
