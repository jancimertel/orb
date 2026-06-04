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
