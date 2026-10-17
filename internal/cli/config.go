package cli

import (
	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/fault"
)

func (a *application) configure(verb string) error {
	settings := map[string]string{"output": a.settings.Output, "policy-id": a.settings.PolicyID, "currency": a.settings.Currency, "timeout": a.settings.Timeout}
	if verb == "list" {
		return a.write(settings)
	}
	key := *a.values["key"]
	value, ok := settings[key]
	if !ok {
		return fault.Usage("Unknown setting. Choose output, policy-id, currency, or timeout.")
	}
	if verb == "get" {
		return a.write(map[string]string{"key": key, "value": value})
	}
	if !a.flags.Changed("value") {
		return fault.Usage("Missing required flag: --value. An empty value clears an optional setting.")
	}
	value = *a.values["value"]
	stored, err := a.store.LoadSettings()
	if err != nil {
		return err
	}
	switch key {
	case "output":
		stored.Output = value
	case "policy-id":
		stored.PolicyID = value
	case "currency":
		stored.Currency = value
	case "timeout":
		stored.Timeout = value
	}
	if value == "" && key != "policy-id" {
		return fault.Usage("This setting cannot be empty.")
	}
	if err := config.ValidateSettings(stored); err != nil {
		return err
	}
	if a.opts.dryRun {
		return a.write(map[string]any{"dryRun": true, "command": "config set", "key": key, "value": value})
	}
	if err := a.store.SaveSettings(stored); err != nil {
		return err
	}
	return a.write(map[string]string{"key": key, "value": value})
}
