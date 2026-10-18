package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/gosolu/clapbot/internal/config"
)

// RunOnboarding runs the interactive huh setup wizard (`clapbot onboard`) and saves ~/.clapbot/config.toml.
func RunOnboarding(homeDir string) error {
	cfg, err := config.Load(homeDir)
	if err != nil {
		return err
	}

	defProf := cfg.Profiles[cfg.DefaultProfile]
	providerType := defProf.Provider
	if providerType == "" {
		providerType = "openai"
	}
	modelName := defProf.Model
	if modelName == "" {
		modelName = "gpt-4o"
	}
	apiKey := defProf.APIKey
	baseURL := defProf.BaseURL
	workspace := cfg.Workspace

	enableTelegram := cfg.Channels.Telegram.Enabled
	tgToken := cfg.Channels.Telegram.BotToken
	tgUsers := strings.Join(cfg.Channels.Telegram.AllowedUsers, ",")

	enableFeishu := cfg.Channels.Feishu.Enabled
	fsAppID := cfg.Channels.Feishu.AppID
	fsAppSecret := cfg.Channels.Feishu.AppSecret
	fsUsers := strings.Join(cfg.Channels.Feishu.AllowedUsers, ",")

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Default LLM Provider").
				Description("Select your primary model provider (powered by gosolu/react)").
				Options(
					huh.NewOption("OpenAI / OpenAI-Compatible", "openai"),
					huh.NewOption("Anthropic (Claude)", "anthropic"),
					huh.NewOption("DeepSeek (V3 / R1 / Flash)", "deepseek"),
					huh.NewOption("OpenCode Go Gateway", "opencode"),
				).
				Value(&providerType),
			huh.NewInput().
				Title("Model Identifier").
				Description("e.g. gpt-4o, claude-sonnet-4-5, deepseek-chat, deepseek-reasoner").
				Value(&modelName),
			huh.NewInput().
				Title("API Key (or ${ENV_VAR})").
				EchoMode(huh.EchoModePassword).
				Value(&apiKey),
			huh.NewInput().
				Title("Custom Base URL (optional)").
				Description("Leave blank for official provider endpoint").
				Value(&baseURL),
			huh.NewInput().
				Title("Default Workspace Directory").
				Value(&workspace),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Enable Telegram Bot Channel?").
				Value(&enableTelegram),
			huh.NewInput().
				Title("Telegram Bot Token").
				Description("From @BotFather (or ${TELEGRAM_BOT_TOKEN})").
				Value(&tgToken),
			huh.NewInput().
				Title("Telegram Allowed Users (comma-separated user IDs or @usernames)").
				Value(&tgUsers),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Enable Feishu / Lark Bot Channel?").
				Description("Uses WebSocket long-connection mode (no public IP needed)").
				Value(&enableFeishu),
			huh.NewInput().
				Title("Feishu App ID").
				Value(&fsAppID),
			huh.NewInput().
				Title("Feishu App Secret").
				EchoMode(huh.EchoModePassword).
				Value(&fsAppSecret),
			huh.NewInput().
				Title("Feishu Allowed OpenIDs (comma-separated, optional)").
				Value(&fsUsers),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.ProfileConfig{}
	}
	cfg.Profiles[cfg.DefaultProfile] = config.ProfileConfig{
		Provider: strings.TrimSpace(providerType),
		Model:    strings.TrimSpace(modelName),
		APIKey:   strings.TrimSpace(apiKey),
		BaseURL:  strings.TrimSpace(baseURL),
	}
	cfg.Workspace = config.ExpandPath(workspace)

	cfg.Channels.Telegram.Enabled = enableTelegram
	cfg.Channels.Telegram.BotToken = strings.TrimSpace(tgToken)
	cfg.Channels.Telegram.AllowedUsers = splitCSV(tgUsers)

	cfg.Channels.Feishu.Enabled = enableFeishu
	cfg.Channels.Feishu.AppID = strings.TrimSpace(fsAppID)
	cfg.Channels.Feishu.AppSecret = strings.TrimSpace(fsAppSecret)
	cfg.Channels.Feishu.AllowedUsers = splitCSV(fsUsers)

	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("✔ Saved configuration to %s\n", cfg.ConfigPath())
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
