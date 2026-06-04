package claude

import (
	"context"
	"encoding/json"
	"fmt"
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

// recordingRunner captures every command invocation and can be told to fail
// specific install/marketplace calls.
type recordingRunner struct {
	calls           [][]string
	failMarketplace bool
	failInstall     map[string]bool
}

func (r *recordingRunner) run(ctx context.Context, home, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	if len(args) >= 2 && args[0] == "plugin" && args[1] == "marketplace" {
		if r.failMarketplace {
			return []byte("marketplace boom"), fmt.Errorf("marketplace add failed")
		}
	}
	if len(args) >= 3 && args[0] == "plugin" && args[1] == "install" {
		if r.failInstall[args[2]] {
			return []byte("install boom"), fmt.Errorf("install failed")
		}
	}
	return nil, nil
}

func baseConfig(home string) PluginConfig {
	return PluginConfig{
		CLIPath:           "claude",
		HomeDir:           home,
		MarketplaceSource: "anthropics/claude-plugins-official",
		Plugins: []string{
			"superpowers@claude-plugins-official",
			"skill-creator@claude-plugins-official",
		},
	}
}

func installCalls(calls [][]string) []string {
	var out []string
	for _, c := range calls {
		if len(c) >= 3 && c[0] == "plugin" && c[1] == "install" {
			out = append(out, c[2])
		}
	}
	return out
}

func TestEnsurePlugins_FreshInstall(t *testing.T) {
	home := t.TempDir()
	rr := &recordingRunner{}

	if err := ensurePlugins(context.Background(), baseConfig(home), rr.run); err != nil {
		t.Fatal(err)
	}

	// Marketplace registered from its configured source.
	if len(rr.calls) == 0 ||
		rr.calls[0][0] != "plugin" || rr.calls[0][1] != "marketplace" ||
		rr.calls[0][2] != "add" || rr.calls[0][3] != "anthropics/claude-plugins-official" {
		t.Fatalf("first call = %v, want plugin marketplace add anthropics/claude-plugins-official", rr.calls[0])
	}
	// Both plugins installed.
	got := installCalls(rr.calls)
	if len(got) != 2 {
		t.Fatalf("install calls = %v, want 2", got)
	}
	// Both enabled in settings.json.
	enabled, _ := readSettings(t, home)["enabledPlugins"].(map[string]any)
	if enabled["superpowers@claude-plugins-official"] != true ||
		enabled["skill-creator@claude-plugins-official"] != true {
		t.Errorf("enabledPlugins = %v, want both true", enabled)
	}
}

func TestEnsurePlugins_SkipsInstalled(t *testing.T) {
	home := t.TempDir()
	writeInstalledPlugins(t, home, "superpowers@claude-plugins-official")
	rr := &recordingRunner{}

	if err := ensurePlugins(context.Background(), baseConfig(home), rr.run); err != nil {
		t.Fatal(err)
	}

	got := installCalls(rr.calls)
	if len(got) != 1 || got[0] != "skill-creator@claude-plugins-official" {
		t.Fatalf("install calls = %v, want only skill-creator", got)
	}
	// Both the already-installed and the newly-installed plugin are enabled.
	enabled, _ := readSettings(t, home)["enabledPlugins"].(map[string]any)
	if enabled["superpowers@claude-plugins-official"] != true {
		t.Errorf("already-installed plugin not enabled: %v", enabled)
	}
	if enabled["skill-creator@claude-plugins-official"] != true {
		t.Errorf("newly-installed plugin not enabled: %v", enabled)
	}
}

func TestEnsurePlugins_InstallFailureIsNonFatal(t *testing.T) {
	home := t.TempDir()
	rr := &recordingRunner{failInstall: map[string]bool{"superpowers@claude-plugins-official": true}}

	if err := ensurePlugins(context.Background(), baseConfig(home), rr.run); err != nil {
		t.Fatalf("install failure must be non-fatal, got %v", err)
	}

	// The failed plugin is NOT enabled; the succeeding one IS.
	enabled, _ := readSettings(t, home)["enabledPlugins"].(map[string]any)
	if enabled["superpowers@claude-plugins-official"] == true {
		t.Errorf("failed plugin should not be enabled: %v", enabled)
	}
	if enabled["skill-creator@claude-plugins-official"] != true {
		t.Errorf("succeeding plugin should be enabled: %v", enabled)
	}
}

func TestEnsurePlugins_MarketplaceFailureIsNonFatal(t *testing.T) {
	home := t.TempDir()
	rr := &recordingRunner{failMarketplace: true}

	if err := ensurePlugins(context.Background(), baseConfig(home), rr.run); err != nil {
		t.Fatalf("marketplace failure must be non-fatal, got %v", err)
	}
	// Installs are still attempted despite the marketplace add failing.
	if len(installCalls(rr.calls)) != 2 {
		t.Errorf("expected installs attempted after marketplace failure: %v", rr.calls)
	}
}

func TestEnsurePlugins_NoPlugins(t *testing.T) {
	home := t.TempDir()
	rr := &recordingRunner{}
	cfg := baseConfig(home)
	cfg.Plugins = nil

	if err := ensurePlugins(context.Background(), cfg, rr.run); err != nil {
		t.Fatal(err)
	}
	if len(rr.calls) != 0 {
		t.Errorf("no plugins → no commands, got %v", rr.calls)
	}
}
