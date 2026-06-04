# Per-chat Effort/Thinking Setting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let each Telegram chat pick the Claude model's reasoning effort level (`low/medium/high/xhigh/max`), applied via the CLI's `--effort` flag, mirroring the existing `/model` feature.

**Architecture:** Add an `Effort` field that flows `chat_state.active_effort` → `TurnOpts.Effort` → `SpawnOpts.Effort` → `--effort <level>` on spawn. Empty means omit the flag (model default). A `/effort` Telegram command + inline-keyboard picker persists the choice and tears down the runner so the next turn respawns with the new flag.

**Tech Stack:** Go, SQLite (`modernc.org/sqlite` via `internal/state`), telego (Telegram), Claude Code CLI subprocess.

**Spec:** `docs/superpowers/specs/2026-06-04-effort-thinking-setting-design.md`

---

### Task 1: Extract a pure arg-builder and add `--effort` to the spawn args

The CLI args are currently built inline in `Spawn`, which is only exercised through the fake-exec integration harness. Extract a pure `buildArgs` function so the flag logic is unit-testable, then add `Effort`.

**Files:**
- Modify: `internal/claude/runner.go`
- Create: `internal/claude/args_test.go`

- [ ] **Step 1: Add the `Effort` field to `SpawnOpts`**

In `internal/claude/runner.go`, add the field right after `Model` in the `SpawnOpts` struct:

```go
	Model     string // optional; maps to --model
	Effort    string // optional; maps to --effort (low|medium|high|xhigh|max). Empty omits the flag.
	SessionID string // optional; maps to --resume
```

- [ ] **Step 2: Extract `buildArgs` from `Spawn`**

In `internal/claude/runner.go`, replace the inline arg construction inside `Spawn` (the block from `args := []string{` through the `--append-system-prompt` append, ending just before `cmd := exec.Command(...)`) with a call to a new helper:

```go
	args, err := buildArgs(opts)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(cli, args...)
```

Then add this new function (place it just below `Spawn`, above `buildEnv`):

```go
// buildArgs assembles the CLI argument list for a spawn. Pure and
// side-effect-free so the flag wiring can be unit-tested without exec.
func buildArgs(opts SpawnOpts) ([]string, error) {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-mode", "acceptEdits",
	}
	if opts.HookBinary != "" {
		settings, err := buildHookSettings(opts.HookBinary)
		if err != nil {
			return nil, err
		}
		args = append(args, "--settings", settings)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.SystemPromptAppend != "" {
		args = append(args, "--append-system-prompt", opts.SystemPromptAppend)
	}
	return args, nil
}
```

Note: `cli` (the resolved `opts.CLIPath` / `"claude"` default) stays in `Spawn` — `buildArgs` returns only the args after the binary name. The `err` variable now declared by `buildArgs` is reused by the existing `stdin, err := cmd.StdinPipe()` line (change that to `stdin, err =` if the compiler reports a redeclaration; otherwise leave as is).

- [ ] **Step 3: Write the failing test**

Create `internal/claude/args_test.go`:

```go
package claude

import "testing"

// hasFlagValue reports whether args contains flag immediately followed by value.
func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// hasFlag reports whether args contains flag at all.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func TestBuildArgs_EffortSet(t *testing.T) {
	args, err := buildArgs(SpawnOpts{Model: "claude-opus-4-8", Effort: "xhigh"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if !hasFlagValue(args, "--effort", "xhigh") {
		t.Errorf("expected --effort xhigh in %v", args)
	}
	if !hasFlagValue(args, "--model", "claude-opus-4-8") {
		t.Errorf("expected --model claude-opus-4-8 in %v", args)
	}
}

func TestBuildArgs_EffortOmittedWhenEmpty(t *testing.T) {
	args, err := buildArgs(SpawnOpts{Model: "claude-opus-4-8"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if hasFlag(args, "--effort") {
		t.Errorf("did not expect --effort in %v", args)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/claude/ -run TestBuildArgs -v`
Expected: PASS for both `TestBuildArgs_EffortSet` and `TestBuildArgs_EffortOmittedWhenEmpty`.

- [ ] **Step 5: Verify the whole package still builds and integration tests pass**

Run: `go test ./internal/claude/`
Expected: PASS (the existing `TestIntegration_*` fake-exec tests still pass).

- [ ] **Step 6: Commit**

```bash
git add internal/claude/runner.go internal/claude/args_test.go
git commit -m "feat(claude): add --effort spawn flag via testable buildArgs"
```

---

### Task 2: Thread `Effort` through the registry

**Files:**
- Modify: `internal/claude/registry.go`

- [ ] **Step 1: Add `Effort` to `TurnOpts`**

In `internal/claude/registry.go`, add to the `TurnOpts` struct after `Model`:

```go
	CWD       string // workspace path; defaults to RegistryConfig.ScratchDir
	Model     string // defaults to RegistryConfig.DefaultModel
	Effort    string // optional; maps to --effort. Empty = model default.
	SessionID string // passed as --resume on a fresh spawn
```

- [ ] **Step 2: Pass it into the spawn and log line**

In `StartTurn`, add `Effort` to the `Spawn(SpawnOpts{...})` literal (after `Model: model,`):

```go
			Model:              model,
			Effort:             opts.Effort,
			SessionID:          opts.SessionID,
```

And add it to the "claude runner spawned" log call (after `"model", model,`):

```go
			"model", model,
			"effort", opts.Effort,
```

- [ ] **Step 3: Build**

Run: `go build ./internal/claude/`
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add internal/claude/registry.go
git commit -m "feat(claude): thread Effort through registry TurnOpts"
```

---

### Task 3: Persist `active_effort` in chat state

**Files:**
- Create: `internal/state/migrations/0003_active_effort.sql`
- Modify: `internal/state/queries.go`
- Create: `internal/state/queries_test.go`

- [ ] **Step 1: Add the migration**

Create `internal/state/migrations/0003_active_effort.sql`:

```sql
ALTER TABLE chat_state ADD COLUMN active_effort TEXT;
```

- [ ] **Step 2: Add the struct field**

In `internal/state/queries.go`, add `ActiveEffort` to `ChatState` after `ActiveAgent`:

```go
	ActiveModel      string
	ActiveAgent      string
	ActiveEffort     string
	UpdatedAt        time.Time
```

- [ ] **Step 3: Read the column in `GetChatState`**

Update the SELECT to include `active_effort` (before `updated_at`) and the `Scan` to match:

```go
	row := s.db.QueryRowContext(ctx, `
		SELECT chat_id, COALESCE(active_repo_alias, ''), COALESCE(active_session_id, ''),
		       COALESCE(active_model, ''), COALESCE(active_agent, ''),
		       COALESCE(active_effort, ''), COALESCE(updated_at, '')
		FROM chat_state WHERE chat_id = ?`, chatID)
```

```go
	if err := row.Scan(&cs.ChatID, &cs.ActiveRepoAlias, &cs.ActiveSessionID, &cs.ActiveModel, &cs.ActiveAgent, &cs.ActiveEffort, &updatedAt); err != nil {
```

- [ ] **Step 4: Add the setter**

After `SetActiveAgent`, add:

```go
func (s *Store) SetActiveEffort(ctx context.Context, chatID int64, effort string) error {
	return s.upsertChatState(ctx, chatID, "active_effort", effort)
}
```

- [ ] **Step 5: Write the failing round-trip test**

Create `internal/state/queries_test.go`:

```go
package state

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSetActiveEffort_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	const chatID = int64(123)

	// Default is empty before any set.
	if err := st.SetActiveModel(ctx, chatID, "claude-opus-4-8"); err != nil {
		t.Fatalf("SetActiveModel: %v", err)
	}
	cs, err := st.GetChatState(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatState: %v", err)
	}
	if cs.ActiveEffort != "" {
		t.Errorf("expected empty effort, got %q", cs.ActiveEffort)
	}

	// After set, it round-trips.
	if err := st.SetActiveEffort(ctx, chatID, "xhigh"); err != nil {
		t.Fatalf("SetActiveEffort: %v", err)
	}
	cs, err = st.GetChatState(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatState: %v", err)
	}
	if cs.ActiveEffort != "xhigh" {
		t.Errorf("expected effort xhigh, got %q", cs.ActiveEffort)
	}
	if cs.ActiveModel != "claude-opus-4-8" {
		t.Errorf("model clobbered: got %q", cs.ActiveModel)
	}
}
```

Note: confirm `Store` has a `Close` method. If it does not, drop the `t.Cleanup` line that calls `st.Close()` (the temp dir is removed automatically). Check with: `grep -n "func (s \*Store) Close" internal/state/*.go`.

- [ ] **Step 6: Run the test**

Run: `go test ./internal/state/ -run TestSetActiveEffort_RoundTrip -v`
Expected: PASS (migration `0003` applies, column reads/writes correctly).

- [ ] **Step 7: Commit**

```bash
git add internal/state/migrations/0003_active_effort.sql internal/state/queries.go internal/state/queries_test.go
git commit -m "feat(state): persist active_effort in chat_state"
```

---

### Task 4: Pass the stored effort into each turn

**Files:**
- Modify: `internal/telegram/turn.go:35-93`

- [ ] **Step 1: Read and pass `ActiveEffort`**

In `driveTurn` (`internal/telegram/turn.go`), the `model` is resolved around line 35. Just pass `cs.ActiveEffort` straight through — no default substitution (empty stays empty). In the `StartTurn` call, add `Effort` after `Model`:

```go
	turn, err := r.registry.StartTurn(chatID, claude.TurnOpts{
		CWD:                cwd,
		Model:              model,
		Effort:             cs.ActiveEffort,
		SessionID:          cs.ActiveSessionID,
		SystemPromptAppend: agentBody,
	})
```

- [ ] **Step 2: Add it to the turn logger**

In the `r.logger.With(...)` block above, add after `"model", model,`:

```go
		"model", model,
		"effort", cs.ActiveEffort,
```

- [ ] **Step 3: Build**

Run: `go build ./internal/telegram/`
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add internal/telegram/turn.go
git commit -m "feat(telegram): apply per-chat effort to turns"
```

---

### Task 5: `/effort` command, picker, and status line

Mirror the existing `/model` machinery in the same file.

**Files:**
- Modify: `internal/telegram/handlers_runtime.go`

- [ ] **Step 1: Add the effort catalog and constants**

In `internal/telegram/handlers_runtime.go`, just below the `availableModels` block / `modelEntry` type, add:

```go
// Effort levels accepted by the CLI's --effort flag. xhigh falls back to high
// on older models; max is unconstrained. Empty (not listed) = model default.
var availableEfforts = []effortEntry{
	{id: "low", label: "Low"},
	{id: "medium", label: "Medium"},
	{id: "high", label: "High"},
	{id: "xhigh", label: "X-High"},
	{id: "max", label: "Max"},
}

type effortEntry struct {
	id    string
	label string
}

const (
	effortPrefix    = "effort:"
	effortActionSet = "set"
)
```

- [ ] **Step 2: Register the command and callback**

In `registerRuntime`, add after the `/model` registrations:

```go
	h.Handle(r.handleEffort, th.CommandEqual("effort"))
	h.Handle(r.handleEffortCallback, th.CallbackDataPrefix(effortPrefix))
```

- [ ] **Step 3: Add the handlers**

Add after the `/model` handlers (after `activeModelFor`):

```go
// --- /effort --------------------------------------------------------------

func (r *Router) handleEffort(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))

	if arg != "" {
		return r.setEffort(ctx, chatID, arg)
	}

	active := r.activeEffortFor(ctx, chatID)

	var rows [][]telego.InlineKeyboardButton
	for _, e := range availableEfforts {
		label := e.label
		if e.id == active {
			label = "✓ " + label
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label, CallbackData: effortPrefix + effortActionSet + ":" + e.id},
		})
	}

	_, err := r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        "Current effort: " + effortDisplay(active) + "\n\nSelect:",
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) handleEffortCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	if !strings.HasPrefix(cq.Data, effortPrefix) {
		return nil
	}
	rest := cq.Data[len(effortPrefix):]
	action, id, ok := strings.Cut(rest, ":")
	if !ok || action != effortActionSet {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	if err := r.applyEffortChange(ctx, chatID, id); err != nil {
		return r.answerCallback(ctx, cq.ID, err.Error())
	}
	_ = r.answerCallback(ctx, cq.ID, "set")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      "Effort set to " + id + "\n(takes effect on next turn)",
		})
	}
	return nil
}

func (r *Router) setEffort(ctx *th.Context, chatID int64, id string) error {
	if err := r.applyEffortChange(ctx, chatID, id); err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	return r.reply(ctx, chatID, "effort set to "+id+"\n(takes effect on next turn)")
}

// applyEffortChange validates id against the known levels, persists it, and
// tears down the runner so the next spawn picks up the new --effort.
func (r *Router) applyEffortChange(ctx *th.Context, chatID int64, id string) error {
	if !knownEffort(id) {
		return fmt.Errorf("unknown effort %q", id)
	}
	if err := r.store.SetActiveEffort(ctx, chatID, id); err != nil {
		r.logger.Error("set effort failed", "chat_id", chatID, "err", err)
		return fmt.Errorf("persist failed")
	}
	r.registry.Reset(chatID)
	r.sessionAllow.Clear(chatID)
	return nil
}

func knownEffort(id string) bool {
	for _, e := range availableEfforts {
		if e.id == id {
			return true
		}
	}
	return false
}

func (r *Router) activeEffortFor(ctx *th.Context, chatID int64) string {
	cs, err := r.chatStateOrEmpty(ctx, chatID)
	if err == nil {
		return cs.ActiveEffort
	}
	return ""
}

// effortDisplay renders an empty effort as the model-default sentinel.
func effortDisplay(effort string) string {
	if effort == "" {
		return "model default"
	}
	return effort
}
```

- [ ] **Step 4: Add `/effort` to the help text and `/status`**

In `helpText`, in the Runtime section, add a line under `/model`:

```
  /model [id]       List models or switch
  /effort [level]   Reasoning effort: low|medium|high|xhigh|max (blank=default)
```

In `handleStatus`, after the `model:` line, add:

```go
	fmt.Fprintf(&sb, "model:    %s\n", r.activeModelFor(ctx, chatID))
	fmt.Fprintf(&sb, "effort:   %s\n", effortDisplay(r.activeEffortFor(ctx, chatID)))
```

- [ ] **Step 5: Write the failing test for `knownEffort`**

Create `internal/telegram/effort_test.go`:

```go
package telegram

import "testing"

func TestKnownEffort(t *testing.T) {
	for _, id := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !knownEffort(id) {
			t.Errorf("expected %q to be known", id)
		}
	}
	for _, id := range []string{"", "ultra", "HIGH", "none"} {
		if knownEffort(id) {
			t.Errorf("expected %q to be unknown", id)
		}
	}
}

func TestEffortDisplay(t *testing.T) {
	if got := effortDisplay(""); got != "model default" {
		t.Errorf("empty: got %q", got)
	}
	if got := effortDisplay("xhigh"); got != "xhigh" {
		t.Errorf("xhigh: got %q", got)
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/telegram/ -run 'TestKnownEffort|TestEffortDisplay' -v`
Expected: PASS.

- [ ] **Step 7: Build the whole project**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 8: Commit**

```bash
git add internal/telegram/handlers_runtime.go internal/telegram/effort_test.go
git commit -m "feat(telegram): add /effort command, picker, and status line"
```

---

### Task 6: Full verification

- [ ] **Step 1: Run the full test suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: no output (success).

- [ ] **Step 3: Manual smoke (optional, requires a running bot)**

In a chat: `/effort` → tap `X-High` → `/status` shows `effort: xhigh` → send a message → bot logs show `effort=xhigh` on "turn started" / "claude runner spawned". `/effort` with no prior choice → `/status` shows `effort: model default` and no `--effort` is passed.

---

## Notes for the implementer

- Helpers `argAfterCommand`, `callbackChatID`, `answerCallback`, `chatStateOrEmpty`, and `reply` already exist in `internal/telegram` and are used identically by the `/model` handlers — reuse them, don't reimplement.
- Do not add a `RegistryConfig.DefaultEffort` or any config var; "no choice" must mean "omit the flag" so each model uses its own default (per spec, Out of scope).
- The `availableEfforts` order is the button order; keep `low → max` ascending.
