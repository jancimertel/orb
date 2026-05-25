package claude

import (
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
