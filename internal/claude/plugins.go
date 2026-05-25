package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// pluginInstalled reports whether pluginKey ("<plugin>@<marketplace>") appears
// in $HOME/.claude/plugins/installed_plugins.json. A missing file means "not
// installed" rather than an error, so a fresh volume reads cleanly.
func pluginInstalled(homeDir, pluginKey string) (bool, error) {
	path := filepath.Join(homeDir, ".claude", "plugins", "installed_plugins.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var doc struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return false, err
	}
	_, ok := doc.Plugins[pluginKey]
	return ok, nil
}

// ensureEnabledPlugins idempotently sets enabledPlugins["<key>"]=true for each
// plugin in $HOME/.claude/settings.json, creating the file if absent and
// preserving every other key. No-op when plugins is empty. This makes plugin
// enablement robust regardless of whether `claude plugin install` writes the
// flag itself.
func ensureEnabledPlugins(homeDir string, plugins []string) error {
	if len(plugins) == 0 {
		return nil
	}
	path := filepath.Join(homeDir, ".claude", "settings.json")

	settings := map[string]any{}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("parse settings.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	enabled, _ := settings["enabledPlugins"].(map[string]any)
	if enabled == nil {
		enabled = map[string]any{}
	}
	for _, p := range plugins {
		enabled[p] = true
	}
	settings["enabledPlugins"] = enabled

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
