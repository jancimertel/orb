# Per-chat effort / thinking setting

**Date:** 2026-06-04
**Status:** Approved, pending implementation

## Goal

Let each Telegram chat choose the Claude model's reasoning *effort* level, the
same way it already chooses the model. Mirror the existing `/model` feature end
to end: a `/effort` command, an inline-keyboard picker, per-chat persistence,
runner teardown on change, and a `/status` line.

## Background

The bot spawns the Claude Code CLI per chat (`internal/claude/runner.go`). The
CLI controls reasoning effort with a `--effort <level>` flag whose accepted
values are `low | medium | high | xhigh | max`. This composes with `--model` and
works in `--print --output-format stream-json` mode. `--effort` is the cleanest
mechanism for us because it is a per-spawn flag exactly parallel to `--model`;
we deliberately do not use the `CLAUDE_CODE_EFFORT_LEVEL` env var or the
`effortLevel` settings.json key.

Notes on values: `xhigh` falls back to `high` on 4.6-era models, and `max` is
"session-only" in the CLI's own settings sense — but both are valid as a
per-turn flag, so we expose all five and let the CLI handle fallback.

## Design

### Mechanism

Pass `--effort <level>` on spawn when the chat has chosen a level. When the chat
has *not* chosen one, omit the flag entirely so each model applies its own
default (e.g. Opus 4.8 = `high`). There is no operator-configurable default.

### 1. CLI plumbing — `internal/claude`

`runner.go`:
- Add `Effort string` to `SpawnOpts` (doc: optional; maps to `--effort`).
- In `Spawn`, after the `--model` block, append `"--effort", opts.Effort` when
  `opts.Effort != ""`.

`registry.go`:
- Add `Effort string` to `TurnOpts` (doc: defaults to none = model default).
- Thread `opts.Effort` into the `SpawnOpts` literal in `StartTurn`.
- Add `"effort", opts.Effort` to the "claude runner spawned" log line.

There is no `RegistryConfig.DefaultEffort`; an empty effort means "omit the
flag".

### 2. Persistence — `internal/state`

- New migration `migrations/0003_active_effort.sql`:
  `ALTER TABLE chat_state ADD COLUMN active_effort TEXT;`
- `ChatState`: add `ActiveEffort string`.
- `GetChatState`: add `COALESCE(active_effort, '')` to the SELECT and a matching
  `&cs.ActiveEffort` in `Scan` (placed consistently with the column order).
- Add `SetActiveEffort(ctx, chatID, effort)` delegating to the existing
  `upsertChatState(ctx, chatID, "active_effort", effort)` helper.

### 3. Turn wiring — `internal/telegram/turn.go`

In `driveTurn`, read `cs.ActiveEffort` and pass it as `TurnOpts.Effort`. Add
`"effort", cs.ActiveEffort` to the turn logger fields. No default substitution —
empty stays empty.

### 4. Telegram UX — `internal/telegram/handlers_runtime.go`

Clone the `/model` machinery:

- `availableEfforts []effortEntry` catalog with the five levels:
  `low, medium, high, xhigh, max` (each `{id, label}`).
- Constants `effortPrefix = "effort:"`, reuse a `set` action like `modelActionSet`.
- `handleEffort`: no arg → send an inline keyboard listing the five levels with
  a `✓` prefix on the active one, plus a header showing the current value or
  "model default" when unset; with an arg → `setEffort` directly.
- `handleEffortCallback`: parse `effort:set:<id>`, call `applyEffortChange`,
  answer the callback, edit the message to confirm.
- `setEffort`: validate + persist + reply.
- `applyEffortChange(ctx, chatID, id)`: reject unknown ids via `knownEffort(id)`;
  `store.SetActiveEffort`; `registry.Reset(chatID)`; `sessionAllow.Clear(chatID)`
  — identical teardown to a model switch so the next turn respawns with the new
  flag.
- `knownEffort(id) bool` over `availableEfforts`.
- `activeEffortFor(ctx, chatID) string`: returns `cs.ActiveEffort` (may be `""`).
- Register `/effort` and the `effort:` callback in `registerRuntime`.
- Add a `/effort [level]` line to the Runtime section of `helpText`.
- Add an `effort:` line to `handleStatus` output, rendering `""` as
  `model default`.

### 5. Tests

- `internal/claude/runner_test.go` (or the existing spawn-args test): assert the
  arg list contains `--effort xhigh` when `SpawnOpts.Effort` is set, and contains
  no `--effort` when it is empty.
- `internal/state/queries_test.go` (or config_test analog): round-trip
  `SetActiveEffort` → `GetChatState` returns the stored value; default is `""`.

## Out of scope (YAGNI)

- `CLAUDE_CODE_EFFORT_LEVEL` env var and `effortLevel` settings.json paths.
- Operator-configurable default effort (`RegistryConfig.DefaultEffort` / config).
- Per-model validation of which levels are supported — the CLI already falls
  back (`xhigh → high`) on older models.

## Acceptance criteria

- `/effort` with no arg shows a picker marking the current level (or "model
  default").
- Selecting a level persists it, tears down the runner, and the next turn spawns
  with `--effort <level>`.
- `/effort` with an unknown arg returns an error and changes nothing.
- A chat that never set effort spawns with no `--effort` flag.
- `/status` shows the active effort (or "model default").
- New + existing tests pass.
