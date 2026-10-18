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
