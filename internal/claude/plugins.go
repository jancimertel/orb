package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

// PluginConfig configures EnsurePlugins.
type PluginConfig struct {
	CLIPath           string   // path to the claude CLI (e.g. "claude")
	HomeDir           string   // HOME for the CLI; also where plugin state lives (/data)
	MarketplaceSource string   // source for `claude plugin marketplace add` (e.g. "anthropics/claude-plugins-official")
	Plugins           []string // "<plugin>@<marketplace>" keys to install + enable
	Logger            *slog.Logger
}

// commandRunner runs an external command with HOME=home and returns combined
// output. Swapped out in tests.
type commandRunner func(ctx context.Context, home, name string, args ...string) ([]byte, error)

// execRunner is the production commandRunner: it runs the binary with HOME set.
func execRunner(ctx context.Context, home, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	return cmd.CombinedOutput()
}

// EnsurePlugins registers the configured marketplace source and installs +
// enables each configured plugin onto $HOME. Idempotent: already-installed
// plugins are skipped. All failures are logged and non-fatal — the bot must
// start even if plugin provisioning fails. ctx bounds the (potentially
// network-bound, first-boot-only) CLI calls so shutdown stays responsive.
func EnsurePlugins(ctx context.Context, cfg PluginConfig) error {
	return ensurePlugins(ctx, cfg, execRunner)
}

func ensurePlugins(ctx context.Context, cfg PluginConfig, run commandRunner) error {
	if len(cfg.Plugins) == 0 {
		return nil
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// 1. Register the marketplace from its source so installs can resolve.
	//    Best-effort: a prior boot may already have registered it.
	mctx, mcancel := context.WithTimeout(ctx, 60*time.Second)
	if out, err := run(mctx, cfg.HomeDir, cfg.CLIPath, "plugin", "marketplace", "add", cfg.MarketplaceSource); err != nil {
		logger.Warn("plugin marketplace add failed", "source", cfg.MarketplaceSource, "err", err, "output", string(out))
	}
	mcancel()

	// 2. Install each plugin that isn't already present; track what's present.
	var present []string
	for _, p := range cfg.Plugins {
		installed, err := pluginInstalled(cfg.HomeDir, p)
		if err != nil {
			// Couldn't read install state (e.g. corrupt installed_plugins.json):
			// fall through and attempt install rather than skip, so an
			// unreadable state file never silently leaves a plugin missing.
			logger.Warn("plugin install-state check failed", "plugin", p, "err", err)
		}
		if installed {
			logger.Debug("plugin already installed; skipping", "plugin", p)
			present = append(present, p)
			continue
		}
		ictx, icancel := context.WithTimeout(ctx, 120*time.Second)
		out, err := run(ictx, cfg.HomeDir, cfg.CLIPath, "plugin", "install", p, "--scope", "user")
		icancel()
		if err != nil {
			logger.Warn("plugin install failed", "plugin", p, "err", err, "output", string(out))
			continue
		}
		logger.Info("plugin installed", "plugin", p)
		present = append(present, p)
	}

	// 3. Ensure every present plugin is enabled in settings.json.
	if err := ensureEnabledPlugins(cfg.HomeDir, present); err != nil {
		logger.Warn("ensure enabledPlugins failed", "err", err)
	}
	return nil
}
