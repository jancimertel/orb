package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestListPlugins(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{
		"version": 2,
		"plugins": {
			"superpowers@claude-plugins-official": [{"scope":"user","version":"5.1.0"}],
			"skill-creator@claude-plugins-official": [{"scope":"user","version":"unknown"}]
		}
	}`)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
		"enabledPlugins": {"superpowers@claude-plugins-official": true},
		"theme": "dark"
	}`)

	got, err := ListPlugins(home)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	// Sorted by key: skill-creator before superpowers.
	if got[0].Key != "skill-creator@claude-plugins-official" {
		t.Errorf("got[0].Key = %q", got[0].Key)
	}
	if got[0].Enabled {
		t.Errorf("skill-creator should be disabled (not in enabledPlugins)")
	}
	if got[1].Key != "superpowers@claude-plugins-official" || !got[1].Enabled {
		t.Errorf("got[1] = %+v, want superpowers enabled", got[1])
	}
	if got[1].Version != "5.1.0" || got[1].Scope != "user" {
		t.Errorf("got[1] version/scope = %q/%q", got[1].Version, got[1].Scope)
	}
}

func TestListPlugins_MissingFileIsEmpty(t *testing.T) {
	got, err := ListPlugins(t.TempDir())
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}
