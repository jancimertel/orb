package usage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jancimertel/orb/internal/claude"
	"github.com/jancimertel/orb/internal/state"
)

func newStore(t *testing.T) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRecordResult_Accumulates(t *testing.T) {
	store := newStore(t)
	tracker := NewTracker(store)
	ctx := context.Background()
	const chatID = int64(42)

	first := &claude.Event{
		Type:         "result",
		TotalCostUSD: 0.01,
		Usage: &claude.Usage{
			InputTokens:              10,
			OutputTokens:             5,
			CacheReadInputTokens:     7,
			CacheCreationInputTokens: 2,
		},
	}
	if err := tracker.RecordResult(ctx, chatID, first); err != nil {
		t.Fatal(err)
	}
	second := &claude.Event{
		Type:         "result",
		TotalCostUSD: 0.02,
		Usage: &claude.Usage{
			InputTokens:              3,
			OutputTokens:             1,
			CacheReadInputTokens:     0,
			CacheCreationInputTokens: 0,
		},
	}
	if err := tracker.RecordResult(ctx, chatID, second); err != nil {
		t.Fatal(err)
	}

	today, err := store.UsageToday(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if today.InputTokens != 13 || today.OutputTokens != 6 {
		t.Errorf("tokens: %+v", today)
	}
	if today.CacheReadTokens != 7 || today.CacheWriteTokens != 2 {
		t.Errorf("cache: %+v", today)
	}
	if today.CostUSD != 0.03 {
		t.Errorf("cost: %v, want 0.03", today.CostUSD)
	}

	mtd, err := store.UsageMonthToDate(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if mtd.CostUSD != 0.03 {
		t.Errorf("mtd cost: %v", mtd.CostUSD)
	}
}

func TestRecordResult_NilUsageSafe(t *testing.T) {
	store := newStore(t)
	tracker := NewTracker(store)
	ctx := context.Background()

	// A result event with no usage block still records cost.
	ev := &claude.Event{Type: "result", TotalCostUSD: 0.05}
	if err := tracker.RecordResult(ctx, 1, ev); err != nil {
		t.Fatal(err)
	}
	today, err := store.UsageToday(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if today.CostUSD != 0.05 || today.InputTokens != 0 {
		t.Errorf("row: %+v", today)
	}
}

func TestRecordResult_NilTrackerAndEventNoop(t *testing.T) {
	// The tracker guards against nil self and nil event so callers don't
	// need defensive checks on the hot path.
	var tracker *Tracker
	if err := tracker.RecordResult(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	}
	store := newStore(t)
	real := NewTracker(store)
	if err := real.RecordResult(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	}
}
