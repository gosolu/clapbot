package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLayoutAndLoadTOML(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "sk-test-123")

	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.DefaultProfile != "default" {
		t.Fatalf("expected default_profile=default, got %q", cfg.DefaultProfile)
	}
	defProf, ok := cfg.Profiles["default"]
	if !ok {
		t.Fatalf("missing default profile")
	}
	if defProf.APIKey != "sk-test-123" {
		t.Fatalf("expected expanded APIKey sk-test-123, got %q", defProf.APIKey)
	}

	for _, name := range []string{"SOUL.md", "USER.md", "MEMORY.md", "config.toml"} {
		if _, err := os.Stat(filepath.Join(tmp, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}

	// Test Save round-trip
	cfg.GatewayAddr = "127.0.0.1:9999"
	if err := Save(cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	reloaded, err := Load(tmp)
	if err != nil {
		t.Fatalf("Reload after Save failed: %v", err)
	}
	if reloaded.GatewayAddr != "127.0.0.1:9999" {
		t.Fatalf("expected saved gateway_addr=127.0.0.1:9999, got %q", reloaded.GatewayAddr)
	}
}

func TestLegacyYAMLCompat(t *testing.T) {
	tmp := t.TempDir()
	// Create legacy config.yaml only
	yamlContent := `default_profile: fast
gateway_addr: 127.0.0.1:8888
profiles:
  fast:
    provider: openai
    model: gpt-4o-mini
    api_key: sk-legacy-key
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(yamlContent), 0600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load legacy YAML failed: %v", err)
	}
	if cfg.DefaultProfile != "fast" {
		t.Fatalf("expected default_profile=fast, got %q", cfg.DefaultProfile)
	}
	if cfg.Profiles["fast"].APIKey != "sk-legacy-key" {
		t.Fatalf("expected sk-legacy-key, got %q", cfg.Profiles["fast"].APIKey)
	}
}

func TestConfigExampleTOML(t *testing.T) {
	examplePath := filepath.Join("..", "..", "config.example.toml")
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("read config.example.toml: %v", err)
	}

	tmp := t.TempDir()
	targetPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(targetPath, data, 0600); err != nil {
		t.Fatalf("write temp config.toml: %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-test-key")
	t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-key")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-key")
	t.Setenv("GEMINI_API_KEY", "sk-gemini-key")

	cfg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load config.example.toml failed: %v", err)
	}

	if cfg.DefaultProfile != "default" {
		t.Errorf("expected default profile 'default', got %q", cfg.DefaultProfile)
	}
	if len(cfg.Profiles) < 4 {
		t.Errorf("expected at least 4 profiles, got %d", len(cfg.Profiles))
	}
	if len(cfg.Subagents) != 2 {
		t.Errorf("expected 2 subagents, got %d", len(cfg.Subagents))
	}
}

