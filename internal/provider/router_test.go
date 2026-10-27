package provider

import (
	"context"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/react"
	"github.com/gosolu/react/provider/mock"
)

func TestRouterProfilesAndFallback(t *testing.T) {
	cfg := &config.Config{
		DefaultProfile: "default",
		Profiles: map[string]config.ProfileConfig{
			"default": {
				Provider: "mock",
				Model:    "mock-gpt",
			},
			"fast": {
				Provider: "mock",
				Model:    "mock-mini",
			},
		},
	}

	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	mockDefault := mock.New(mock.Text("response from default"))
	mockFast := mock.New(mock.Text("response from fast"))
	router.RegisterProfile("default", cfg.Profiles["default"], mockDefault)
	router.RegisterProfile("fast", cfg.Profiles["fast"], mockFast)

	if !router.HasProfile("default") || !router.HasProfile("fast") {
		t.Fatalf("expected router to have default and fast profiles")
	}
	if router.HasProfile("nonexistent") {
		t.Fatalf("expected false for nonexistent profile")
	}

	profiles := router.Profiles()
	if len(profiles) != 2 || profiles[0] != "default" || profiles[1] != "fast" {
		t.Fatalf("unexpected profiles: %v", profiles)
	}

	ctx := context.Background()

	// Call with fast profile
	respFast, err := router.Generate(ctx, &react.Request{
		Model: "fast",
		Messages: []react.Message{
			react.UserText("hello"),
		},
	})
	if err != nil {
		t.Fatalf("Generate(fast): %v", err)
	}
	if respFast.Message.Text() != "response from fast" {
		t.Fatalf("expected 'response from fast', got %q", respFast.Message.Text())
	}

	// Call with empty profile (should fallback to default)
	respDef, err := router.Generate(ctx, &react.Request{
		Model: "",
		Messages: []react.Message{
			react.UserText("hello"),
		},
	})
	if err != nil {
		t.Fatalf("Generate(default fallback): %v", err)
	}
	if respDef.Message.Text() != "response from default" {
		t.Fatalf("expected 'response from default', got %q", respDef.Message.Text())
	}
}

func TestBuildProviderGemini(t *testing.T) {
	// Native Gemini API
	pNative, err := buildProvider(config.ProfileConfig{
		Provider: "gemini",
		APIKey:   "test-key",
	})
	if err != nil {
		t.Fatalf("buildProvider(gemini native): %v", err)
	}
	if pNative.Name() != "gemini" {
		t.Fatalf("expected provider name 'gemini', got %q", pNative.Name())
	}

	// OpenAI-compatible Chat Completions endpoint
	pChat, err := buildProvider(config.ProfileConfig{
		Provider: "gemini",
		APIKey:   "test-key",
		WireAPI:  "chat",
	})
	if err != nil {
		t.Fatalf("buildProvider(gemini chat): %v", err)
	}
	if pChat.Name() != "gemini" {
		t.Fatalf("expected provider name 'gemini', got %q", pChat.Name())
	}

	// Provider alias "google"
	pGoogle, err := buildProvider(config.ProfileConfig{
		Provider: "google",
		APIKey:   "test-key",
	})
	if err != nil {
		t.Fatalf("buildProvider(google alias): %v", err)
	}
	if pGoogle.Name() != "gemini" {
		t.Fatalf("expected provider name 'gemini', got %q", pGoogle.Name())
	}

	// API key fallback from GEMINI_API_KEY env
	t.Setenv("GEMINI_API_KEY", "env-gemini-key")
	pEnv, err := buildProvider(config.ProfileConfig{
		Provider: "gemini",
	})
	if err != nil {
		t.Fatalf("buildProvider(gemini env fallback): %v", err)
	}
	if pEnv.Name() != "gemini" {
		t.Fatalf("expected provider name 'gemini', got %q", pEnv.Name())
	}

	// End-to-end NewRouter with gemini profile
	cfg := &config.Config{
		DefaultProfile: "gemini",
		Profiles: map[string]config.ProfileConfig{
			"gemini": {
				Provider: "gemini",
				Model:    "gemini-3.8-flash",
				APIKey:   "test-gemini-key",
			},
		},
	}
	r, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter with gemini profile: %v", err)
	}
	if !r.HasProfile("gemini") {
		t.Fatalf("expected router to have gemini profile")
	}
	desc, ok := r.Describe("gemini")
	if !ok || desc.Model != "gemini-3.8-flash" || desc.Provider != "gemini" {
		t.Fatalf("unexpected describe info: %+v", desc)
	}
}
