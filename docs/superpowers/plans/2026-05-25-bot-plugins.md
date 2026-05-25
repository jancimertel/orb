# Bot Plugins Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the orb bot's spawned Claude Code subprocess load three pure-skill plugins (`superpowers`, `skill-creator`, `code-simplifier`) from the `claude-plugins-official` marketplace.

**Architecture:** The marketplace catalog is baked into the image at build time at a non-volume path (`/opt/bot/marketplace`). At startup the bot installs and enables the configured plugins onto the `/data` volume by shelling out to the `claude` CLI (`EnsurePlugins`). Plugin enablement is written to `/data/.claude/settings.json`, which the per-turn runner's inline `--settings` merges with rather than replaces.

**Tech Stack:** Go 1.25, `caarlos0/env/v11` for config, `os/exec` for the `claude` CLI, standard `testing`. Docker (`node:22-slim` runtime stage).

**Spec:** [docs/superpowers/specs/2026-05-25-bot-plugins-design.md](../specs/2026-05-25-bot-plugins-design.md)

> **Revision during execution (2026-05-25):** Task 6 (Dockerfile bake) was
> reverted and Tasks 2/4 adjusted. The official marketplace name is reserved by
> the CLI and only accepts the `anthropics` GitHub source, not a baked local
> path. The marketplace is therefore added from GitHub at startup (network on
> first boot only); `MarketplaceDir`/`PluginMarketplaceDir` became
> `MarketplaceSource`/`PluginMarketplaceSource`. See the spec's Revision note.

---

## File Structure

- **Create** `internal/claude/plugins.go` — `PluginConfig`, `EnsurePlugins`, and the unexported helpers `ensurePlugins`, `pluginInstalled`, `ensureEnabledPlugins`, `execRunner`. One responsibility: provision plugins at startup via the CLI.
- **Create** `internal/claude/plugins_test.go` — unit tests for the helpers and orchestration, using an injected fake command runner and temp HOME dirs.
- **Create** `internal/config/config_test.go` — tests for the new config fields' default and override.
- **Modify** `internal/config/config.go` — add `EnabledPlugins []string` and `PluginMarketplaceDir string`.
- **Modify** `cmd/bot/main.go` — call `claude.EnsurePlugins(...)` right after `agent.Seed(...)`.
- **Modify** `Dockerfile` — add a build step that fetches the marketplace catalog and relocates it to `/opt/bot/marketplace`.
- **Modify** `examples/README.md` — document the plugin behavior and `ENABLE_PLUGINS`.

---

## Task 1: Config fields for plugins

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoad_EnabledPlugins -v`
Expected: compile error / FAIL — `cfg.EnabledPlugins` and `cfg.PluginMarketplaceDir` undefined.

- [ ] **Step 3: Add the config fields**

In `internal/config/config.go`, add these fields to the `Config` struct, right after the `ApprovalHookBin` field:

```go
	// EnabledPlugins is the set of Claude Code plugins the bot installs and
	// enables for spawned subprocesses on startup. Each entry is a
	// "<plugin>@<marketplace>" key. It is the single source of truth: the
	// startup step installs each one and writes it into enabledPlugins in
	// $HOME/.claude/settings.json. Override to add/remove without rebuilding
	// the image (the marketplace catalog already ships the full set).
	EnabledPlugins []string `env:"ENABLE_PLUGINS" envSeparator:"," envDefault:"superpowers@claude-plugins-official,skill-creator@claude-plugins-official,code-simplifier@claude-plugins-official"`

	// PluginMarketplaceDir is the image-baked, non-volume path holding the
	// claude-plugins-official marketplace catalog (see Dockerfile). The
	// startup step registers this local path so installs run offline.
	PluginMarketplaceDir string `env:"PLUGIN_MARKETPLACE_DIR" envDefault:"/opt/bot/marketplace"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestLoad_EnabledPlugins -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): EnabledPlugins and PluginMarketplaceDir"
```

---

## Task 2: `pluginInstalled` idempotency check

**Files:**
- Create: `internal/claude/plugins.go`
- Test: `internal/claude/plugins_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/claude/plugins_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claude/ -run TestPluginInstalled -v`
Expected: compile error — `pluginInstalled` undefined.

- [ ] **Step 3: Create `plugins.go` with `pluginInstalled`**

Create `internal/claude/plugins.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claude/ -run TestPluginInstalled -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/claude/plugins.go internal/claude/plugins_test.go
git commit -m "feat(claude): pluginInstalled idempotency check"
```

---

## Task 3: `ensureEnabledPlugins` settings merge

**Files:**
- Modify: `internal/claude/plugins.go`
- Test: `internal/claude/plugins_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/claude/plugins_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claude/ -run TestEnsureEnabledPlugins -v`
Expected: compile error — `ensureEnabledPlugins` undefined.

- [ ] **Step 3: Add `ensureEnabledPlugins`**

Append to `internal/claude/plugins.go` (and add `"fmt"` to the import block):

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claude/ -run TestEnsureEnabledPlugins -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add internal/claude/plugins.go internal/claude/plugins_test.go
git commit -m "feat(claude): ensureEnabledPlugins settings merge"
```

---

## Task 4: `ensurePlugins` orchestration + public `EnsurePlugins`

**Files:**
- Modify: `internal/claude/plugins.go`
- Test: `internal/claude/plugins_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/claude/plugins_test.go` (add `"context"` and `"fmt"` to its imports):

```go
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
		CLIPath:        "claude",
		HomeDir:        home,
		MarketplaceDir: "/opt/bot/marketplace",
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

	if err := ensurePlugins(baseConfig(home), rr.run); err != nil {
		t.Fatal(err)
	}

	// Marketplace registered with the baked local path.
	if len(rr.calls) == 0 ||
		rr.calls[0][0] != "plugin" || rr.calls[0][1] != "marketplace" ||
		rr.calls[0][2] != "add" || rr.calls[0][3] != "/opt/bot/marketplace" {
		t.Fatalf("first call = %v, want plugin marketplace add /opt/bot/marketplace", rr.calls[0])
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

	if err := ensurePlugins(baseConfig(home), rr.run); err != nil {
		t.Fatal(err)
	}

	got := installCalls(rr.calls)
	if len(got) != 1 || got[0] != "skill-creator@claude-plugins-official" {
		t.Fatalf("install calls = %v, want only skill-creator", got)
	}
	// Already-installed plugin is still enabled.
	enabled, _ := readSettings(t, home)["enabledPlugins"].(map[string]any)
	if enabled["superpowers@claude-plugins-official"] != true {
		t.Errorf("already-installed plugin not enabled: %v", enabled)
	}
}

func TestEnsurePlugins_InstallFailureIsNonFatal(t *testing.T) {
	home := t.TempDir()
	rr := &recordingRunner{failInstall: map[string]bool{"superpowers@claude-plugins-official": true}}

	if err := ensurePlugins(baseConfig(home), rr.run); err != nil {
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

	if err := ensurePlugins(baseConfig(home), rr.run); err != nil {
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

	if err := ensurePlugins(cfg, rr.run); err != nil {
		t.Fatal(err)
	}
	if len(rr.calls) != 0 {
		t.Errorf("no plugins → no commands, got %v", rr.calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claude/ -run TestEnsurePlugins -v`
Expected: compile error — `PluginConfig`, `ensurePlugins`, `commandRunner` undefined.

- [ ] **Step 3: Add `PluginConfig`, `commandRunner`, `execRunner`, `ensurePlugins`, `EnsurePlugins`**

Append to `internal/claude/plugins.go` and update its import block to:

```go
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
```

Then add:

```go
// PluginConfig configures EnsurePlugins.
type PluginConfig struct {
	CLIPath        string   // path to the claude CLI (e.g. "claude")
	HomeDir        string   // HOME for the CLI; also where plugin state lives (/data)
	MarketplaceDir string   // image-baked local marketplace catalog (/opt/bot/marketplace)
	Plugins        []string // "<plugin>@<marketplace>" keys to install + enable
	Logger         *slog.Logger
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

// EnsurePlugins registers the baked marketplace and installs + enables each
// configured plugin onto $HOME. Idempotent: already-installed plugins are
// skipped. All failures are logged and non-fatal — the bot must start even if
// plugin provisioning fails.
func EnsurePlugins(cfg PluginConfig) error {
	return ensurePlugins(cfg, execRunner)
}

func ensurePlugins(cfg PluginConfig, run commandRunner) error {
	if len(cfg.Plugins) == 0 {
		return nil
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// 1. Register the baked local marketplace so installs resolve offline.
	//    Best-effort: a prior boot may already have registered it.
	mctx, mcancel := context.WithTimeout(context.Background(), 60*time.Second)
	if out, err := run(mctx, cfg.HomeDir, cfg.CLIPath, "plugin", "marketplace", "add", cfg.MarketplaceDir); err != nil {
		logger.Warn("plugin marketplace add failed", "dir", cfg.MarketplaceDir, "err", err, "output", string(out))
	}
	mcancel()

	// 2. Install each plugin that isn't already present; track what's present.
	var present []string
	for _, p := range cfg.Plugins {
		installed, err := pluginInstalled(cfg.HomeDir, p)
		if err != nil {
			logger.Warn("plugin install-state check failed", "plugin", p, "err", err)
		}
		if installed {
			logger.Debug("plugin already installed; skipping", "plugin", p)
			present = append(present, p)
			continue
		}
		ictx, icancel := context.WithTimeout(context.Background(), 120*time.Second)
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/claude/ -run TestEnsurePlugins -v`
Expected: PASS (all five tests).

- [ ] **Step 5: Run the full claude package test to check for regressions**

Run: `go test ./internal/claude/ -v`
Expected: PASS (existing runner/integration tests plus the new plugin tests).

- [ ] **Step 6: Commit**

```bash
git add internal/claude/plugins.go internal/claude/plugins_test.go
git commit -m "feat(claude): EnsurePlugins startup install + enable"
```

---

## Task 5: Wire `EnsurePlugins` into startup

**Files:**
- Modify: `cmd/bot/main.go:143-145`

- [ ] **Step 1: Add the call after `agent.Seed`**

In `cmd/bot/main.go`, immediately after the `agent.Seed(...)` block (the one logging `"seed examples failed"`, around line 145), add:

```go
	// Install and enable the configured Claude Code plugins onto the
	// persistent .claude volume. Runs at startup (not build) because plugins
	// live under $HOME/.claude/plugins, which is the claude-home volume and
	// shadows any image-baked content. Non-fatal: the bot starts regardless.
	if err := claude.EnsurePlugins(claude.PluginConfig{
		CLIPath:        cfg.ClaudeCLI,
		HomeDir:        cfg.HomeDir,
		MarketplaceDir: cfg.PluginMarketplaceDir,
		Plugins:        cfg.EnabledPlugins,
		Logger:         logger,
	}); err != nil {
		logger.Warn("ensure plugins failed", "err", err)
	}
```

(The `claude` package is already imported in `main.go`.)

- [ ] **Step 2: Verify it builds**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 3: Run the whole test suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 4: Commit**

```bash
git add cmd/bot/main.go
git commit -m "feat(bot): provision plugins at startup"
```

---

## Task 6: Dockerfile — bake the marketplace catalog

**Files:**
- Modify: `Dockerfile`

- [ ] **Step 1: Add the marketplace-bake step**

In `Dockerfile`, after the large `RUN apt-get ... npm install -g @anthropic-ai/claude-code ... chown -R bot:bot /data` block (ends around line 46) and before `COPY --from=build /out/bot ...`, insert a new build step:

```dockerfile
# Pre-fetch the official plugin marketplace catalog into a non-volume image
# path. The bot registers this local path at startup and installs the
# configured plugins from it (see internal/claude/plugins.go). It must live
# outside /data because /data/.claude is a named volume that shadows
# image-baked content at runtime. Staging HOME keeps /data pristine.
RUN HOME=/tmp/plugin-seed claude plugin marketplace add anthropics/claude-plugins-official \
 && mkdir -p /opt/bot \
 && mv /tmp/plugin-seed/.claude/plugins/marketplaces/claude-plugins-official /opt/bot/marketplace \
 && rm -rf /tmp/plugin-seed \
 && chown -R bot:bot /opt/bot/marketplace
```

- [ ] **Step 2: Build the image to verify the step succeeds**

Run: `docker build --platform linux/amd64 -t orb:plugins-test .`
Expected: build completes; the marketplace step prints progress and exits 0. (Requires Docker + network for the marketplace fetch.)

> If `claude plugin marketplace add anthropics/claude-plugins-official` errors (auth/interactive/unknown source), this is **Verification item 1/5** from the spec. Resolve before continuing: confirm the correct non-interactive add syntax and that no API auth is required, then update this step.

- [ ] **Step 3: Confirm the catalog landed at the expected path**

Run:
```bash
docker run --rm --platform linux/amd64 orb:plugins-test \
  sh -c 'ls /opt/bot/marketplace/.claude-plugin/ && cat /opt/bot/marketplace/.claude-plugin/marketplace.json | head -c 200'
```
Expected: lists `marketplace.json`; the JSON shows `"name": "claude-plugins-official"` (confirms the name resolves for `<plugin>@claude-plugins-official` installs).

- [ ] **Step 4: Commit**

```bash
git add Dockerfile
git commit -m "build: bake plugin marketplace catalog into image"
```

---

## Task 7: End-to-end verification + docs

**Files:**
- Modify: `examples/README.md`

- [ ] **Step 1: Verify offline install works inside the image**

Run (uses a throwaway HOME inside the container — no compose volume needed):
```bash
docker run --rm --platform linux/amd64 -e HOME=/tmp/verify orb:plugins-test sh -c '
  mkdir -p /tmp/verify &&
  claude plugin marketplace add /opt/bot/marketplace &&
  claude plugin install superpowers@claude-plugins-official --scope user &&
  echo "--- installed_plugins.json ---" &&
  cat /tmp/verify/.claude/plugins/installed_plugins.json'
```
Expected: each command exits 0 (offline — no GitHub fetch); `installed_plugins.json` contains `superpowers@claude-plugins-official`. This confirms Verification items 1, 4, and 5.

> If `--scope user` also wrote `/tmp/verify/.claude/settings.json` with `enabledPlugins`, Verification item 2 holds. Either way `ensureEnabledPlugins` guarantees enablement, so no code change is required here — just note the observed behavior in the spec's verification section.

- [ ] **Step 2: Full bot smoke test (manual)**

With a populated `.env`, run `docker compose up --build`. From Telegram, start a session and send a message that should pick up a skill (e.g. ask the bot to do something a superpowers skill covers). In the bot logs, confirm:
- `"plugin installed"` log lines for the 3 plugins on first boot (and none on a second boot — idempotent).
- The spawned Claude's `system/init` event lists the 3 plugins as loaded. (Inspect via `LOG_LEVEL=debug` or by adding a temporary log of the raw init event.)

Expected: plugins load; a superpowers skill is invocable in the chat.

> This is the real gate (spec "Integration / manual"). If `system/init` does not list the plugins, check that the runner's inline `--settings` is not clobbering the file-based `enabledPlugins` (Verification item 3); if it is, switch the runner to merge or move enablement entirely into settings.json.

- [ ] **Step 2b: Document the behavior**

In `examples/README.md`, add a short section:

```markdown
## Plugins

On startup the bot installs and enables a set of Claude Code plugins for every
spawned session, from the `claude-plugins-official` marketplace baked into the
image. Defaults: `superpowers`, `skill-creator`, `code-simplifier`.

Override the set with the `ENABLE_PLUGINS` env var (comma-separated
`<plugin>@<marketplace>` keys); the marketplace ships the full catalog, so no
rebuild is needed to change which plugins are active — just restart. Plugin
files persist on the `claude-home` volume under `.claude/plugins`.
```

- [ ] **Step 3: Commit**

```bash
git add examples/README.md
git commit -m "docs: document bot plugin behavior and ENABLE_PLUGINS"
```

---

## Self-Review notes

- **Spec coverage:** Component 1 (build marketplace) → Task 6. Component 2 (startup install + enable, incl. enablement fallback) → Tasks 2-5. Component 3 (runner unchanged) → no task by design; Step 2 of Task 7 verifies the merge assumption. Component 4 (config) → Task 1. Testing section → Tasks 1-4 (unit) + Task 7 (integration). Verification items 1/4/5 → Task 6-7 build/run steps; item 2 → Task 7 Step 1 note; item 3 → Task 7 Step 2 note.
- **Idempotency:** `pluginInstalled` guards re-installs; `ensureEnabledPlugins` is a merge; marketplace add is best-effort. Re-runs on later boots are no-ops.
- **Type consistency:** `PluginConfig`, `commandRunner`, `ensurePlugins`, `pluginInstalled`, `ensureEnabledPlugins`, `EnsurePlugins` are used identically across Tasks 2-5. Config fields `EnabledPlugins` / `PluginMarketplaceDir` match between Task 1 and the Task 5 wiring.
</content>
