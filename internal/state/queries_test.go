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
