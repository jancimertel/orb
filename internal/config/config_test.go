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

func TestLoad_EnabledPluginsDefault(t *testing.T) {
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
	if len(cfg.EnabledPlugins) != len(want) {
		t.Fatalf("EnabledPlugins = %v, want %v", cfg.EnabledPlugins, want)
	}
	for i, p := range want {
		if cfg.EnabledPlugins[i] != p {
			t.Errorf("EnabledPlugins[%d] = %q, want %q", i, cfg.EnabledPlugins[i], p)
		}
	}

	if cfg.PluginMarketplaceDir != "/opt/bot/marketplace" {
		t.Errorf("PluginMarketplaceDir = %q, want /opt/bot/marketplace", cfg.PluginMarketplaceDir)
	}
}

func TestLoad_EnabledPluginsOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ENABLE_PLUGINS", "superpowers@claude-plugins-official,foo@bar")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"superpowers@claude-plugins-official", "foo@bar"}
	if len(cfg.EnabledPlugins) != len(want) {
		t.Fatalf("EnabledPlugins = %v, want %v", cfg.EnabledPlugins, want)
	}
	for i, p := range want {
		if cfg.EnabledPlugins[i] != p {
			t.Errorf("EnabledPlugins[%d] = %q, want %q", i, cfg.EnabledPlugins[i], p)
		}
	}
}
