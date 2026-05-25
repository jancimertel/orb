package claude

import (
	"encoding/json"
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
