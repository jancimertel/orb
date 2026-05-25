package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeInstalledPlugins writes a minimal installed_plugins.json under
// home/.claude/plugins for the given plugin keys.
func writeInstalledPlugins(t *testing.T, home string, keys ...string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":2,"plugins":{`
	for i, k := range keys {
		if i > 0 {
			doc += ","
		}
		doc += `"` + k + `":[{"scope":"user"}]`
	}
	doc += `}}`
	if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPluginInstalled(t *testing.T) {
	home := t.TempDir()
	writeInstalledPlugins(t, home, "superpowers@claude-plugins-official")

	got, err := pluginInstalled(home, "superpowers@claude-plugins-official")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected installed=true for present plugin")
	}

	got, err = pluginInstalled(home, "skill-creator@claude-plugins-official")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected installed=false for absent plugin")
	}
}

func TestPluginInstalled_NoFile(t *testing.T) {
	got, err := pluginInstalled(t.TempDir(), "superpowers@claude-plugins-official")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got {
		t.Error("expected installed=false when file absent")
	}
}

func readSettings(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnsureEnabledPlugins_CreatesFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ensureEnabledPlugins(home, []string{"superpowers@claude-plugins-official"})
	if err != nil {
		t.Fatal(err)
	}

	m := readSettings(t, home)
	enabled, _ := m["enabledPlugins"].(map[string]any)
	if enabled["superpowers@claude-plugins-official"] != true {
		t.Errorf("enabledPlugins = %v, want superpowers=true", m["enabledPlugins"])
	}
}

func TestEnsureEnabledPlugins_PreservesExisting(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Operator-written settings: a custom key plus one already-enabled plugin.
	existing := `{"model":"opus","enabledPlugins":{"other@mkt":true}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ensureEnabledPlugins(home, []string{"superpowers@claude-plugins-official"})
	if err != nil {
		t.Fatal(err)
	}

	m := readSettings(t, home)
	if m["model"] != "opus" {
		t.Errorf("model key not preserved: %v", m["model"])
	}
	enabled, _ := m["enabledPlugins"].(map[string]any)
	if enabled["other@mkt"] != true {
		t.Errorf("existing enabled plugin dropped: %v", enabled)
	}
	if enabled["superpowers@claude-plugins-official"] != true {
		t.Errorf("new plugin not enabled: %v", enabled)
	}
}

func TestEnsureEnabledPlugins_Empty(t *testing.T) {
	home := t.TempDir()
	// No plugins → no file written, no error.
	if err := ensureEnabledPlugins(home, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("expected no settings.json to be written")
	}
}
