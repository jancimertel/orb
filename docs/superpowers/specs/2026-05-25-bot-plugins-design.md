# Design: Claude Code plugins for the orb bot

Date: 2026-05-25
Status: Approved (pending implementation plan)

## Problem

The orb bot spawns the Claude Code CLI as a subprocess per Telegram chat
([`internal/claude/runner.go`](../../../internal/claude/runner.go)). That
subprocess currently has **no plugins** — none of the marketplace plugins the
operator uses in their desktop sessions (superpowers, skill-creator,
code-simplifier, …) are available to the bot.

We want the bot's spawned Claude to load a curated set of skill-pack plugins so
the bot benefits from the same skills the operator relies on interactively.

## Scope

**In scope — three pure-skill plugins** from the `claude-plugins-official`
marketplace:

- `superpowers@claude-plugins-official`
- `skill-creator@claude-plugins-official`
- `code-simplifier@claude-plugins-official`

These are chosen because they are pure skill packs: no language-server binaries,
no MCP servers, no network or API keys required at runtime.

**Out of scope (deliberately deferred):**

- LSP plugins (`typescript-lsp`, `gopls-lsp`) — require language-server binaries
  in the image.
- MCP plugins (`context7`) — require network and possibly API keys at runtime.
- Per-chat plugin enable/disable UI in Telegram. Plugins are global here.

These can be layered on later once the pipeline below is proven.

## Key constraint: the named volume

`docker-compose.yml` mounts `claude-home:/data/.claude` as a **named volume**,
and `HOME=/data` for the bot and every spawned subprocess. Consequences:

- Anything baked into the image *under `/data/.claude`* (settings, plugins,
  marketplace) is **shadowed** at runtime. Docker copies image content into a
  named volume only the first time the volume is created empty, and never again.
- Session history (`/data/.claude/projects/...`) and OAuth credentials
  (`/data/.claude/.credentials.json`) live in this same volume, so it cannot be
  recreated to refresh plugins without losing those.

Therefore: build-time artifacts that must be *visible at runtime* go to a
**non-volume path** (`/opt/bot/...`), and anything that must live under
`/data/.claude` is produced **at startup** (with `HOME=/data`, writing directly
into the volume).

This is the same reasoning behind orb's existing startup seeder
([`internal/agent/seed.go`](../../../internal/agent/seed.go)), which bakes
example `agents/commands/jobs/skills` at `/opt/bot/examples/.claude` and copies
them into `/data/.claude` on boot.

## Architecture

Split: **the marketplace catalog is provisioned at build; the plugins are
installed and enabled at startup.**

```
BUILD (Dockerfile)                         STARTUP (cmd/bot/main.go)
------------------                         -------------------------
claude plugin marketplace add              claude.EnsurePlugins(cfg):
  anthropics/claude-plugins-official         1. register local marketplace
  (into staging HOME)                           claude plugin marketplace add
relocate catalog ->                              /opt/bot/marketplace   (offline)
  /opt/bot/marketplace  (non-volume)         2. for each cfg.EnabledPlugins:
                                                claude plugin install
                                                  <name>@claude-plugins-official
                                                  --scope user   (HOME=/data)
                                              -> writes plugin cache to
                                                 /data/.claude/plugins/cache/...
                                                 (correct paths by construction)
                                              -> enables via enabledPlugins in
                                                 /data/.claude/settings.json

RUNTIME (per turn)
------------------
runner spawns `claude --print ... --settings <hooks-json>`
  -> Claude reads enabledPlugins from /data/.claude/settings.json
  -> inline --settings (hooks only) MERGES with the file; does not clobber
```

### Component 1 — Build: marketplace only (Dockerfile)

After `npm install -g @anthropic-ai/claude-code`, in the runtime stage:

1. With a staging `HOME` (e.g. `/tmp/plugin-seed`), run
   `claude plugin marketplace add anthropics/claude-plugins-official`.
2. Relocate the fetched marketplace catalog to `/opt/bot/marketplace/`
   (non-volume), `chown bot:bot`.
3. Leave `/data` pristine.

The image now carries the offline catalog, version-pinned at build time. No
plugin *cache* is built here.

### Component 2 — Startup: install + enable (`internal/claude`)

New function `EnsurePlugins(cfg, logger)`, called immediately after
`agent.Seed(...)` in [`cmd/bot/main.go`](../../../cmd/bot/main.go) (~line 143).
It lives in `internal/claude` because it shells out to the `claude` CLI, which
is that package's responsibility. With `HOME=cfg.HomeDir` (`/data`):

1. **Register the baked marketplace** if not already known:
   `claude plugin marketplace add /opt/bot/marketplace` (local path → offline,
   instant). Idempotent.
2. **Install each plugin** in `cfg.EnabledPlugins`:
   `claude plugin install <name>@claude-plugins-official --scope user`.
   - Runs at runtime with the real `HOME=/data`, so the install writes
     `/data/.claude/plugins/cache/...` with correct absolute paths — no path
     normalization needed.
   - `--scope user` enables the plugin by writing `enabledPlugins` into
     `/data/.claude/settings.json` (on the volume, persists).
   - Guarded so re-runs on later boots are no-ops (skip a plugin already present
     in `installed_plugins.json`).

**Enablement fallback:** if `plugin install` does *not* auto-write
`enabledPlugins` (see Verification), `EnsurePlugins` writes the
`enabledPlugins` entries into `/data/.claude/settings.json` directly
(idempotent merge that preserves any operator edits).

### Component 3 — Runner: unchanged

No change to [`runner.go`](../../../internal/claude/runner.go). The spawned
Claude reads `enabledPlugins` from `/data/.claude/settings.json`. The runner's
existing inline `--settings` (hooks only) **merges** with that file rather than
replacing it, so the Bash approval hook and the file's `enabledPlugins`
coexist.

### Component 4 — Config

Add to [`internal/config/config.go`](../../../internal/config/config.go):

```go
EnabledPlugins []string `env:"ENABLE_PLUGINS" envDefault:"superpowers@claude-plugins-official,skill-creator@claude-plugins-official,code-simplifier@claude-plugins-official"`
```

This is the **single source of truth** for which plugins the startup step
installs and enables. Changing the env and restarting yields a different plugin
set with no image rebuild (the marketplace already contains the full catalog).

## Data flow summary

1. Image build → marketplace catalog at `/opt/bot/marketplace` (non-volume).
2. First boot → `EnsurePlugins` registers the local marketplace, installs the 3
   plugins into `/data/.claude/plugins/cache`, enables them in
   `/data/.claude/settings.json`.
3. Later boots → `EnsurePlugins` is a no-op (already installed).
4. Each turn → runner spawns Claude with `HOME=/data`; Claude loads the enabled
   plugins from the volume.

## Error handling

- **Build:** if `marketplace add` fails (network), the build fails loudly —
  acceptable, caught in CI/build.
- **Startup:** `EnsurePlugins` failures are logged at WARN and execution
  continues (mirrors `agent.Seed`'s `logger.Warn` handling). A plugin failure
  must not brick the bot — it just starts without that plugin.
- **Runner:** unchanged; existing settings-marshal error handling applies.

## Testing

- **Unit (`internal/config`):** `EnabledPlugins` parses the default and a
  custom comma-separated `ENABLE_PLUGINS`.
- **Unit (`internal/claude`):** `EnsurePlugins` builds the correct
  `claude plugin ...` argv per configured plugin; idempotency guard skips
  already-installed plugins; failures are non-fatal. Use the existing
  fake-claude harness pattern
  ([`fake_main_test.go`](../../../internal/claude/fake_main_test.go)) to assert
  the commands invoked without a real CLI.
- **Integration / manual (the real gate):** build the image, run the container,
  send a Telegram message, and confirm the `system/init` event lists the 3
  plugins and that a superpowers skill is invocable.

## Verification items (resolve during implementation)

These need a real `claude` CLI (not available in the design sandbox):

1. `claude plugin marketplace add` and `claude plugin install` run headless,
   non-interactively, without extra API auth.
2. `claude plugin install --scope user` writes `enabledPlugins` into
   `/data/.claude/settings.json`. If not → use the enablement fallback in
   Component 2.
3. Inline `--settings` (hooks JSON) merges with file-based `enabledPlugins`
   (does not clobber).
4. `claude plugin install` is idempotent, or the install guard correctly detects
   an already-installed plugin via `installed_plugins.json`.
5. A marketplace registered from a baked local path (`/opt/bot/marketplace`)
   serves installs offline **and keeps the name `claude-plugins-official`**
   (derived from the catalog's `.claude-plugin/marketplace.json`), so that
   `install <name>@claude-plugins-official` resolves. If the local add yields a
   different marketplace name, the install references and `enabledPlugins` keys
   must use that name instead.

## Risks

- **R1 — `--scope user` enable location.** If install does not enable, the
  fallback (write `enabledPlugins` directly) covers it. Low risk.
- **R2 — auth at build/startup.** Marketplace fetch and install are content
  operations and should not need API auth; runtime has credentials regardless.
  Verify item 1.
- **R3 — config/marketplace drift.** `ENABLE_PLUGINS` may name a plugin not in
  the baked marketplace. Mitigation: install failures are non-fatal and logged;
  default stays in sync with the curated set.

## Files touched

- `Dockerfile` — build-time marketplace add + relocate to `/opt/bot/marketplace`.
- `internal/config/config.go` — `EnabledPlugins` field.
- `internal/claude/` — new `EnsurePlugins` (+ tests).
- `cmd/bot/main.go` — call `EnsurePlugins` after `agent.Seed`.
- Docs / README — note the plugin behavior and `ENABLE_PLUGINS`.
</content>
</invoke>
