package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// PluginStatus describes one installed plugin for operator-facing listings.
type PluginStatus struct {
	Key     string // "<plugin>@<marketplace>"
	Version string // reported install version ("" or "unknown" when unset)
	Scope   string // install scope, e.g. "user"
	Enabled bool   // whether enabledPlugins in settings.json has it true
}

// ListPlugins reads the plugin state under $HOME/.claude and returns every
// installed plugin with its enabled flag, sorted by key. A missing
// installed_plugins.json yields an empty list (not an error) so a fresh volume
// reads cleanly.
func ListPlugins(homeDir string) ([]PluginStatus, error) {
	path := filepath.Join(homeDir, ".claude", "plugins", "installed_plugins.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Plugins map[string][]struct {
			Scope   string `json:"scope"`
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse installed_plugins.json: %w", err)
	}

	enabled := readEnabledPlugins(homeDir)
	out := make([]PluginStatus, 0, len(doc.Plugins))
	for key, installs := range doc.Plugins {
		ps := PluginStatus{Key: key, Enabled: enabled[key]}
		if len(installs) > 0 {
			ps.Version = installs[0].Version
			ps.Scope = installs[0].Scope
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// readEnabledPlugins returns the enabledPlugins map from settings.json, or an
// empty map when the file is absent or unparseable (best-effort, never errors).
func readEnabledPlugins(homeDir string) map[string]bool {
	path := filepath.Join(homeDir, ".claude", "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	var doc struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return map[string]bool{}
	}
	return doc.EnabledPlugins
}

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

// reconcileEnabledPlugins rewrites enabledPlugins in $HOME/.claude/settings.json
// so it reflects `desired` EXACTLY: every desired plugin is set true, and every
// other plugin previously listed is set false (disabled). This makes the PLUGINS
// env var authoritative — dropping a plugin from it and restarting disables it on
// the next boot, so only the configured plugins load. All other settings keys are
// preserved. Disabled plugins are written as false (rather than deleted) so the
// intent is explicit and robust regardless of any default-enable behavior.
func reconcileEnabledPlugins(homeDir string, desired []string) error {
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

	desiredSet := make(map[string]bool, len(desired))
	for _, p := range desired {
		desiredSet[p] = true
	}

	prev, _ := settings["enabledPlugins"].(map[string]any)
	enabled := make(map[string]any, len(prev)+len(desired))
	// Flip every previously-listed plugin to its desired membership: anything no
	// longer wanted becomes false (disabled).
	for k := range prev {
		enabled[k] = desiredSet[k]
	}
	// Ensure every desired plugin is present and enabled.
	for _, p := range desired {
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
	CLIPath            string   // path to the claude CLI (e.g. "claude")
	HomeDir            string   // HOME for the CLI; also where plugin state lives (/data)
	MarketplaceSources []string // sources for `claude plugin marketplace add` (e.g. "anthropics/claude-plugins-official")
	Plugins            []string // "<plugin>@<marketplace>" keys to install + enable
	Logger             *slog.Logger
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

	// 1. Register each configured marketplace source so installs can resolve.
	//    Best-effort: a prior boot may already have registered them, and one
	//    bad source must not block the others.
	for _, src := range cfg.MarketplaceSources {
		mctx, mcancel := context.WithTimeout(ctx, 60*time.Second)
		if out, err := run(mctx, cfg.HomeDir, cfg.CLIPath, "plugin", "marketplace", "add", src); err != nil {
			logger.Warn("plugin marketplace add failed", "source", src, "err", err, "output", string(out))
		}
		mcancel()
	}

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

	// 3. Reconcile settings.json so enabledPlugins reflects exactly the
	//    configured-and-installed set (present); anything else is disabled.
	if err := reconcileEnabledPlugins(cfg.HomeDir, present); err != nil {
		logger.Warn("reconcile enabledPlugins failed", "err", err)
	}
	return nil
}
