package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// Config represents the top-level ~/.clapbot/config.toml (or legacy config.yaml) structure.
type Config struct {
	HomeDir        string                     `toml:"-" yaml:"-"`
	DefaultProfile string                     `toml:"default_profile" yaml:"default_profile"`
	Workspace      string                     `toml:"workspace" yaml:"workspace"`
	ContextBudget  int                        `toml:"context_budget" yaml:"context_budget"`
	GatewayAddr    string                     `toml:"gateway_addr" yaml:"gateway_addr"`
	GatewayToken   string                     `toml:"gateway_token,omitempty" yaml:"gateway_token,omitempty"`
	Logging        LoggingConfig              `toml:"logging,omitempty" yaml:"logging,omitempty"`
	Profiles       map[string]ProfileConfig   `toml:"profiles" yaml:"profiles"`
	Permissions    PermissionsConfig          `toml:"permissions" yaml:"permissions"`
	Channels       ChannelsConfig             `toml:"channels" yaml:"channels"`
	Web            WebConfig                  `toml:"web" yaml:"web"`
	Subagents      []SubagentConfig           `toml:"subagents" yaml:"subagents"`
	MCPServers     map[string]MCPServerConfig `toml:"mcp_servers" yaml:"mcp_servers"`
}

// ProfileConfig defines a named LLM provider and model profile.
type ProfileConfig struct {
	Provider string `toml:"provider" yaml:"provider"` // openai | anthropic | deepseek | gemini | opencode | mock
	Model    string `toml:"model" yaml:"model"`
	APIKey   string `toml:"api_key" yaml:"api_key"`
	BaseURL  string `toml:"base_url,omitempty" yaml:"base_url,omitempty"`
	WireAPI  string `toml:"wire_api,omitempty" yaml:"wire_api,omitempty"` // chat | responses
}

// PermissionsConfig defines tool permission policies.
type PermissionsConfig struct {
	Mode  string   `toml:"mode" yaml:"mode"` // allow_all | rules
	Allow []string `toml:"allow,omitempty" yaml:"allow,omitempty"`
	Ask   []string `toml:"ask,omitempty" yaml:"ask,omitempty"`
	Deny  []string `toml:"deny,omitempty" yaml:"deny,omitempty"`
}

// LoggingConfig configures structured logging with xslog.
type LoggingConfig struct {
	Level  string `toml:"level,omitempty" yaml:"level,omitempty"`   // debug | info | warn | error
	Format string `toml:"format,omitempty" yaml:"format,omitempty"` // text | json
	Source bool   `toml:"source,omitempty" yaml:"source,omitempty"` // include source file & line (shortened)
	File   string `toml:"file,omitempty" yaml:"file,omitempty"`     // log file path (e.g. ~/.clapbot/logs/clapbot.log)
}

// ChannelsConfig configures chat app integrations.
type ChannelsConfig struct {
	Telegram TelegramConfig `toml:"telegram" yaml:"telegram"`
	Feishu   FeishuConfig   `toml:"feishu" yaml:"feishu"`
}

// TelegramConfig configures the Telegram bot channel.
type TelegramConfig struct {
	Enabled      bool     `toml:"enabled" yaml:"enabled"`
	BotToken     string   `toml:"bot_token" yaml:"bot_token"`
	AllowedUsers []string `toml:"allowed_users" yaml:"allowed_users"`
	StreamMode   string   `toml:"stream_mode" yaml:"stream_mode"` // edit | final
}

// FeishuConfig configures the Feishu / Lark bot channel.
type FeishuConfig struct {
	Enabled      bool     `toml:"enabled" yaml:"enabled"`
	AppID        string   `toml:"app_id" yaml:"app_id"`
	AppSecret    string   `toml:"app_secret" yaml:"app_secret"`
	Domain       string   `toml:"domain,omitempty" yaml:"domain,omitempty"` // https://open.feishu.cn or https://open.larksuite.com
	AllowedUsers []string `toml:"allowed_users" yaml:"allowed_users"`
	StreamMode   string   `toml:"stream_mode" yaml:"stream_mode"` // edit | final
}

// WebConfig configures web fetch and web search tools.
type WebConfig struct {
	SearchProvider string `toml:"search_provider" yaml:"search_provider"` // duckduckgo | brave | tavily | deepseek
	SearchAPIKey   string `toml:"search_api_key,omitempty" yaml:"search_api_key,omitempty"`
	FetchProvider  string `toml:"fetch_provider,omitempty" yaml:"fetch_provider,omitempty"` // direct | deepseek
	FetchAPIKey    string `toml:"fetch_api_key,omitempty" yaml:"fetch_api_key,omitempty"`
}

// SubagentConfig defines a pre-configured sub-agent exposed via agent.AsTool.
type SubagentConfig struct {
	Name         string `toml:"name" yaml:"name"`
	Description  string `toml:"description" yaml:"description"`
	Profile      string `toml:"profile" yaml:"profile"`
	SystemPrompt string `toml:"system_prompt,omitempty" yaml:"system_prompt,omitempty"`
}

// MCPServerConfig configures an external MCP server over stdio or HTTP/SSE.
type MCPServerConfig struct {
	Command string            `toml:"command,omitempty" yaml:"command,omitempty"`
	Args    []string          `toml:"args,omitempty" yaml:"args,omitempty"`
	Env     map[string]string `toml:"env,omitempty" yaml:"env,omitempty"`
	URL     string            `toml:"url,omitempty" yaml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty" yaml:"headers,omitempty"`
}

// DefaultHomeDir resolves ~/.clapbot (or $CLAPBOT_HOME if set).
func DefaultHomeDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("CLAPBOT_HOME")); env != "" {
		return ExpandPath(env), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".clapbot"), nil
}

// ExpandPath expands leading ~/ to the user's home directory.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// EnsureLayout creates ~/.clapbot subdirectories and default markdown/config files if missing.
func EnsureLayout(homeDir string) error {
	if homeDir == "" {
		var err error
		homeDir, err = DefaultHomeDir()
		if err != nil {
			return err
		}
	}
	dirs := []string{
		homeDir,
		filepath.Join(homeDir, "memory"),
		filepath.Join(homeDir, "skills"),
		filepath.Join(homeDir, "workspace"),
		filepath.Join(homeDir, "logs"),
		filepath.Join(homeDir, "uploads"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}

	files := map[string]string{
		filepath.Join(homeDir, "SOUL.md"):   DefaultSoulMD,
		filepath.Join(homeDir, "USER.md"):   DefaultUserMD,
		filepath.Join(homeDir, "MEMORY.md"): DefaultMemoryMD,
	}
	for path, content := range files {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				return fmt.Errorf("write default %s: %w", path, err)
			}
		}
	}

	// Create config.toml as default if neither config.toml nor legacy config.yaml exists
	tomlPath := filepath.Join(homeDir, "config.toml")
	yamlPath := filepath.Join(homeDir, "config.yaml")
	if _, errTOML := os.Stat(tomlPath); os.IsNotExist(errTOML) {
		if _, errYAML := os.Stat(yamlPath); os.IsNotExist(errYAML) {
			if err := os.WriteFile(tomlPath, []byte(DefaultConfigTOML), 0600); err != nil {
				return fmt.Errorf("write default %s: %w", tomlPath, err)
			}
		}
	}

	return nil
}

// Load reads and parses config.toml (or legacy config.yaml) from homeDir, expanding ${ENV} placeholders.
func Load(homeDir string) (*Config, error) {
	if homeDir == "" {
		var err error
		homeDir, err = DefaultHomeDir()
		if err != nil {
			return nil, err
		}
	}
	homeDir = ExpandPath(homeDir)
	if err := EnsureLayout(homeDir); err != nil {
		return nil, err
	}

	tomlPath := filepath.Join(homeDir, "config.toml")
	yamlPath := filepath.Join(homeDir, "config.yaml")

	var (
		cfgPath string
		isYAML  bool
	)
	if _, err := os.Stat(tomlPath); err == nil {
		cfgPath = tomlPath
	} else if _, err := os.Stat(yamlPath); err == nil {
		cfgPath = yamlPath
		isYAML = true
	} else {
		cfgPath = tomlPath
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", cfgPath, err)
	}

	expanded := expandEnvPlaceholders(string(raw))
	cfg := &Config{
		HomeDir:        homeDir,
		DefaultProfile: "default",
		Workspace:      filepath.Join(homeDir, "workspace"),
		ContextBudget:  64000,
		GatewayAddr:    "127.0.0.1:8765",
		Profiles:       map[string]ProfileConfig{},
		MCPServers:     map[string]MCPServerConfig{},
	}

	if isYAML {
		if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
			return nil, fmt.Errorf("parse yaml config %s: %w", cfgPath, err)
		}
	} else {
		if err := toml.Unmarshal([]byte(expanded), cfg); err != nil {
			return nil, fmt.Errorf("parse toml config %s: %w", cfgPath, err)
		}
	}

	cfg.HomeDir = homeDir
	if cfg.Workspace == "" {
		cfg.Workspace = filepath.Join(homeDir, "workspace")
	} else {
		cfg.Workspace = ExpandPath(cfg.Workspace)
	}
	if cfg.ContextBudget <= 0 {
		cfg.ContextBudget = 64000
	}
	if cfg.GatewayAddr == "" {
		cfg.GatewayAddr = "127.0.0.1:8765"
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "default"
	}
	if cfg.Permissions.Mode == "" {
		cfg.Permissions.Mode = "allow_all"
	}
	if cfg.Channels.Telegram.StreamMode == "" {
		cfg.Channels.Telegram.StreamMode = "edit"
	}
	if cfg.Channels.Feishu.StreamMode == "" {
		cfg.Channels.Feishu.StreamMode = "edit"
	}
	if cfg.Web.SearchProvider == "" {
		cfg.Web.SearchProvider = "duckduckgo"
	}
	return cfg, nil
}

// Save writes the configuration back to homeDir/config.toml.
func Save(cfg *Config) error {
	if cfg.HomeDir == "" {
		var err error
		cfg.HomeDir, err = DefaultHomeDir()
		if err != nil {
			return err
		}
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal toml config: %w", err)
	}
	cfgPath := filepath.Join(cfg.HomeDir, "config.toml")
	return os.WriteFile(cfgPath, data, 0600)
}

// ConfigPath returns the primary configuration file path.
func (c *Config) ConfigPath() string {
	tomlPath := filepath.Join(c.HomeDir, "config.toml")
	if _, err := os.Stat(tomlPath); err == nil {
		return tomlPath
	}
	yamlPath := filepath.Join(c.HomeDir, "config.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}
	return tomlPath
}

// DBPath returns the SQLite database path (~/.clapbot/clapbot.db).
func (c *Config) DBPath() string {
	return filepath.Join(c.HomeDir, "clapbot.db")
}

// SkillsDir returns ~/.clapbot/skills.
func (c *Config) SkillsDir() string {
	return filepath.Join(c.HomeDir, "skills")
}

// UploadsDir returns ~/.clapbot/uploads.
func (c *Config) UploadsDir() string {
	return filepath.Join(c.HomeDir, "uploads")
}

// LogFile returns ~/.clapbot/logs/clapbot.log (or configured logging.file).
func (c *Config) LogFile() string {
	if c.Logging.File != "" {
		return ExpandPath(c.Logging.File)
	}
	return filepath.Join(c.HomeDir, "logs", "clapbot.log")
}

// expandEnvPlaceholders replaces ${VAR} with os.Getenv("VAR") while leaving $VAR untouched.
func expandEnvPlaceholders(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end >= 0 {
				varName := s[i+2 : i+2+end]
				if before, after, ok := strings.Cut(varName, ":-"); ok {
					key := before
					defVal := after
					if val := os.Getenv(key); val != "" {
						out.WriteString(val)
					} else {
						out.WriteString(defVal)
					}
				} else {
					out.WriteString(os.Getenv(varName))
				}
				i += 2 + end + 1
				continue
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}
