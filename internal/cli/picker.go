package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/gosolu/clapbot/internal/gateway"
	"github.com/gosolu/clapbot/internal/store"
)

// IsTTY reports whether stdin/stdout is an interactive terminal.
func IsTTY() bool {
	return isTerminal(os.Stdin.Fd()) && isTerminal(os.Stdout.Fd())
}

// PickSession opens an interactive huh selector to choose a saved session or start a new one.
func PickSession(sessions []store.Session, currentID string) (string, error) {
	if !IsTTY() || len(sessions) == 0 {
		return "", nil
	}
	opts := make([]huh.Option[string], 0, len(sessions)+1)
	opts = append(opts, huh.NewOption("➕ Start a new session (/new)", "__new__"))
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		label := fmt.Sprintf("%s — %s (%d msgs, %s)", s.ID, title, s.MessageCount, s.Profile)
		if s.ID == currentID {
			label = "★ " + label
		}
		opts = append(opts, huh.NewOption(label, s.ID))
	}

	selected := currentID
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Switch Session").
				Description("Select a conversation session to resume or create a new one").
				Options(opts...).
				Value(&selected),
		),
	)
	if err := form.Run(); err != nil {
		return "", err
	}
	return selected, nil
}

// PickModel opens an interactive huh selector to choose a model profile.
func PickModel(profiles []gateway.ProfileItem, currentProfile string) (string, error) {
	if !IsTTY() || len(profiles) == 0 {
		return "", nil
	}
	opts := make([]huh.Option[string], 0, len(profiles))
	for _, p := range profiles {
		label := fmt.Sprintf("%s (%s / %s)", p.Alias, p.Provider, p.Model)
		if p.Alias == currentProfile {
			label = "★ " + label
		}
		opts = append(opts, huh.NewOption(label, p.Alias))
	}

	selected := currentProfile
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Switch Model Profile").
				Description("Select the active model profile for this session").
				Options(opts...).
				Value(&selected),
		),
	)
	if err := form.Run(); err != nil {
		return "", err
	}
	return selected, nil
}
