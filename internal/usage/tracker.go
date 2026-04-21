package usage

import (
	"context"

	"github.com/jancimertel/orb/internal/claude"
	"github.com/jancimertel/orb/internal/state"
)

// Tracker accumulates per-chat token + cost usage onto the state store.
type Tracker struct {
	store *state.Store
}

func NewTracker(store *state.Store) *Tracker {
	return &Tracker{store: store}
}

// RecordResult folds a Claude result event into today's usage row.
// Safe to call with a nil event or nil usage (no-op).
func (t *Tracker) RecordResult(ctx context.Context, chatID int64, ev *claude.Event) error {
	if t == nil || ev == nil {
		return nil
	}
	d := state.UsageDelta{CostUSD: ev.TotalCostUSD}
	if ev.Usage != nil {
		d.InputTokens = ev.Usage.InputTokens
		d.OutputTokens = ev.Usage.OutputTokens
		d.CacheReadTokens = ev.Usage.CacheReadInputTokens
		d.CacheWriteTokens = ev.Usage.CacheCreationInputTokens
	}
	return t.store.AddUsage(ctx, chatID, d)
}
