package claude

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// RegistryConfig holds the shared settings applied to every spawned runner.
type RegistryConfig struct {
	CLIPath        string
	HomeDir        string // HOME env for subprocesses (e.g. /data)
	APIKey         string // ANTHROPIC_API_KEY
	ScratchDir     string // cwd to use when no repo is selected
	DefaultModel   string
	HookBinary     string // absolute path to approvalhook binary
	ApprovalSocket string // unix socket the hook dials
	Logger         *slog.Logger
}

// Registry owns one Runner per chat and enforces "one turn per chat".
type Registry struct {
	cfg RegistryConfig

	mu    sync.Mutex
	slots map[int64]*slot
}

type slot struct {
	runner    *Runner
	busy      bool
	cancelled bool
}

// NewRegistry builds an empty registry.
func NewRegistry(cfg RegistryConfig) *Registry {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Registry{
		cfg:   cfg,
		slots: map[int64]*slot{},
	}
}

// TurnOpts influence only a fresh spawn. When an existing live runner is
// reused, these are ignored (call Reset first to force a respawn).
type TurnOpts struct {
	CWD       string // workspace path; defaults to RegistryConfig.ScratchDir
	Model     string // defaults to RegistryConfig.DefaultModel
	SessionID string // passed as --resume on a fresh spawn

	// SystemPromptAppend is the active agent's body, forwarded to the CLI
	// as --append-system-prompt. Changing it requires registry.Reset so the
	// next turn spawns a fresh process with the new prompt.
	SystemPromptAppend string
}

// StartTurn reserves the turn slot for chatID, spawning a runner if none is
// alive. Returns ErrBusy if another turn is already in flight for this chat.
//
// The returned Turn exposes the live Runner and a Release method that frees
// the slot (call it exactly once via defer).
func (reg *Registry) StartTurn(chatID int64, opts TurnOpts) (*Turn, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	s, ok := reg.slots[chatID]
	if ok && s.busy {
		return nil, ErrBusy
	}

	// Drop dead runners (crash, Close, Cancel from another path).
	if ok && s.runner != nil && !s.runner.Alive() {
		reg.cfg.Logger.Debug("runner no longer alive; will respawn",
			"chat_id", chatID,
			"exit_err", s.runner.ExitError(),
		)
		s = nil
		delete(reg.slots, chatID)
	}

	if s == nil {
		cwd := opts.CWD
		if cwd == "" {
			cwd = reg.cfg.ScratchDir
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			return nil, fmt.Errorf("claude: mkdir cwd: %w", err)
		}
		model := opts.Model
		if model == "" {
			model = reg.cfg.DefaultModel
		}
		r, err := Spawn(SpawnOpts{
			CLIPath:            reg.cfg.CLIPath,
			CWD:                cwd,
			HomeDir:            reg.cfg.HomeDir,
			APIKey:             reg.cfg.APIKey,
			Model:              model,
			SessionID:          opts.SessionID,
			HookBinary:         reg.cfg.HookBinary,
			ApprovalSocket:     reg.cfg.ApprovalSocket,
			ChatID:             chatID,
			SystemPromptAppend: opts.SystemPromptAppend,
		})
		if err != nil {
			return nil, err
		}
		reg.cfg.Logger.Info("claude runner spawned",
			"chat_id", chatID,
			"cwd", cwd,
			"model", model,
			"resume", opts.SessionID != "",
			"system_prompt_append_chars", len(opts.SystemPromptAppend),
		)
		s = &slot{runner: r}
		reg.slots[chatID] = s
	}

	s.busy = true
	s.cancelled = false

	return &Turn{reg: reg, chatID: chatID, slot: s}, nil
}

// Cancel terminates the runner for chatID (if any) and drops it. Safe to call
// with no runner present. Returns true if a runner was cancelled.
func (reg *Registry) Cancel(chatID int64) bool {
	reg.mu.Lock()
	s, ok := reg.slots[chatID]
	if !ok {
		reg.mu.Unlock()
		return false
	}
	delete(reg.slots, chatID)
	if s != nil {
		s.cancelled = true
	}
	reg.mu.Unlock()

	if s != nil && s.runner != nil {
		_ = s.runner.Cancel()
	}
	return true
}

// Reset tears down the runner for chatID so that the next StartTurn spawns
// fresh. Equivalent to Cancel but intended for /new and /model switches.
func (reg *Registry) Reset(chatID int64) {
	reg.Cancel(chatID)
}

// IsBusy reports whether a turn is currently in flight for chatID.
func (reg *Registry) IsBusy(chatID int64) bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	s, ok := reg.slots[chatID]
	return ok && s.busy
}

// HasRunner reports whether a live runner is attached to chatID, regardless
// of whether a turn is currently executing.
func (reg *Registry) HasRunner(chatID int64) bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	s, ok := reg.slots[chatID]
	return ok && s.runner != nil && s.runner.Alive()
}

// Shutdown cancels every active runner. Call on graceful shutdown.
func (reg *Registry) Shutdown() {
	reg.mu.Lock()
	runners := make([]*Runner, 0, len(reg.slots))
	for _, s := range reg.slots {
		if s != nil && s.runner != nil {
			runners = append(runners, s.runner)
		}
	}
	reg.slots = map[int64]*slot{}
	reg.mu.Unlock()
	for _, r := range runners {
		_ = r.Cancel()
	}
}

// Turn is a scoped handle to an acquired chat slot. It is not safe for
// concurrent use; call Release exactly once.
type Turn struct {
	reg    *Registry
	chatID int64
	slot   *slot
}

// Runner returns the live runner for this turn.
func (t *Turn) Runner() *Runner { return t.slot.runner }

// Cancelled reports whether /cancel (or Reset) was called on this chat while
// the turn was in flight.
func (t *Turn) Cancelled() bool {
	t.reg.mu.Lock()
	defer t.reg.mu.Unlock()
	return t.slot.cancelled
}

// Release frees the slot so the next message can start a new turn. Safe to
// call multiple times.
func (t *Turn) Release() {
	t.reg.mu.Lock()
	defer t.reg.mu.Unlock()
	t.slot.busy = false
}
