package config

import (
	"testing"
)

// setRequiredEnv sets the env vars config.Load requires so tests can focus on
// the field under test. t.Setenv auto-restores after the test.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TELEGRAM_BOT_TOKEN", "x")
	t.Setenv("ALLOWED_USER_ID", "1")
	t.Setenv("GITHUB_PAT", "x")
	t.Setenv("GIT_USER_NAME", "x")
	t.Setenv("GIT_USER_EMAIL", "x")
}

func TestLoad_PluginsDefault(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"superpowers@claude-plugins-official",
		"skill-creator@claude-plugins-official",
		"code-simplifier@claude-plugins-official",
	}
	if len(cfg.Plugins) != len(want) {
		t.Fatalf("Plugins = %v, want %v", cfg.Plugins, want)
	}
	for i, p := range want {
		if cfg.Plugins[i] != p {
			t.Errorf("Plugins[%d] = %q, want %q", i, cfg.Plugins[i], p)
		}
	}

	if len(cfg.PluginMarketplaceSources) != 1 ||
		cfg.PluginMarketplaceSources[0] != "anthropics/claude-plugins-official" {
		t.Errorf("PluginMarketplaceSources = %v, want [anthropics/claude-plugins-official]", cfg.PluginMarketplaceSources)
	}
}

func TestLoad_PluginsOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PLUGINS", "superpowers@claude-plugins-official,foo@bar")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"superpowers@claude-plugins-official", "foo@bar"}
	if len(cfg.Plugins) != len(want) {
		t.Fatalf("Plugins = %v, want %v", cfg.Plugins, want)
	}
	for i, p := range want {
		if cfg.Plugins[i] != p {
			t.Errorf("Plugins[%d] = %q, want %q", i, cfg.Plugins[i], p)
		}
	}
}

func TestLoad_MarketplaceSourcesOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PLUGIN_MARKETPLACE_SOURCES", "anthropics/claude-plugins-official,someorg/extra")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"anthropics/claude-plugins-official", "someorg/extra"}
	if len(cfg.PluginMarketplaceSources) != len(want) {
		t.Fatalf("PluginMarketplaceSources = %v, want %v", cfg.PluginMarketplaceSources, want)
	}
	for i, s := range want {
		if cfg.PluginMarketplaceSources[i] != s {
			t.Errorf("PluginMarketplaceSources[%d] = %q, want %q", i, cfg.PluginMarketplaceSources[i], s)
		}
	}
}
