package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/magarcia/expensify-cli/internal/fault"
)

type Settings struct {
	Output   string `json:"output"`
	PolicyID string `json:"policyID,omitempty"`
	Currency string `json:"currency"`
	Timeout  string `json:"timeout"`
}

func DefaultSettings() Settings {
	return Settings{Output: "auto", Currency: "USD", Timeout: "30s"}
}

func (s *Store) LoadSettings() (Settings, error) {
	var settings Settings
	if err := s.read("config.json", &settings); err != nil {
		return Settings{}, err
	}
	return settings, ValidateSettings(settings)
}

func (s *Store) SaveSettings(settings Settings) error {
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	return s.write("config.json", settings)
}

// LoadSettings applies defaults, user configuration, project configuration, then environment settings.
func LoadSettings(store *Store) (Settings, error) {
	settings := DefaultSettings()
	user, err := store.LoadSettings()
	if err != nil {
		return Settings{}, err
	}
	settings = mergeSettings(settings, user)
	projectPath := ".expensify.json"
	info, err := os.Lstat(projectPath)
	if err == nil {
		if !info.Mode().IsRegular() {
			return Settings{}, fault.New("CONFIG-002", "Project configuration must be a regular file, without symbolic links.", "Replace .expensify.json with a regular JSON file.", 1)
		}
		file, err := os.Open(projectPath)
		if err != nil {
			return Settings{}, fileError("read", projectPath)
		}
		var project Settings
		err = decode(file, &project, projectPath)
		closeErr := file.Close()
		if err != nil {
			return Settings{}, err
		}
		if closeErr != nil {
			return Settings{}, fileError("read", projectPath)
		}
		settings = mergeSettings(settings, project)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Settings{}, fileError("inspect", filepath.Base(projectPath))
	}
	settings = mergeSettings(settings, Settings{
		Output: os.Getenv("EXPENSIFY_OUTPUT"), PolicyID: os.Getenv("EXPENSIFY_POLICY_ID"),
		Currency: os.Getenv("EXPENSIFY_CURRENCY"), Timeout: os.Getenv("EXPENSIFY_TIMEOUT"),
	})
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func ValidateSettings(settings Settings) error {
	switch settings.Output {
	case "", "auto", "text", "json", "llm":
	default:
		return fault.Usage("Output must be auto, text, json, or llm.")
	}
	if settings.Timeout != "" {
		duration, err := time.ParseDuration(settings.Timeout)
		if err != nil || duration <= 0 {
			return fault.Usage("Timeout must be a positive duration, such as 30s or 2m.")
		}
	}
	if settings.Currency != "" {
		if len(settings.Currency) != 3 || strings.IndexFunc(settings.Currency, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			return fault.Usage("Currency must be a three-letter uppercase code, such as USD or EUR.")
		}
	}
	return nil
}

func mergeSettings(base, override Settings) Settings {
	if override.Output != "" {
		base.Output = override.Output
	}
	if override.PolicyID != "" {
		base.PolicyID = override.PolicyID
	}
	if override.Currency != "" {
		base.Currency = override.Currency
	}
	if override.Timeout != "" {
		base.Timeout = override.Timeout
	}
	return base
}
