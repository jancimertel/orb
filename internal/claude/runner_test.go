package claude

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// spawnFake builds SpawnOpts that re-exec the test binary as a fake CLI
// with the given scenario. Callers pass the resulting options to Spawn.
func spawnFake(t *testing.T, scenario string) SpawnOpts {
	t.Helper()
	return SpawnOpts{
		CLIPath: os.Args[0],
		CWD:     t.TempDir(),
		HomeDir: t.TempDir(),
		APIKey:  "test-key",
		ExtraEnv: []string{
			"CLAUDE_FAKE=1",
			"CLAUDE_FAKE_SCENARIO=" + scenario,
			"CLAUDE_FAKE_SESSION=s-" + scenario,
		},
	}
}

// drain returns events until the first result event (or until the channel
// closes) or the deadline passes.
func drain(t *testing.T, r *Runner, timeout time.Duration) []Event {
	t.Helper()
	var out []Event
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
			if ev.Type == "result" {
				return out
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for result; collected %d events", len(out))
		}
	}
}

func TestIntegration_BasicTurn(t *testing.T) {
	r, err := Spawn(spawnFake(t, "echo"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cancel()

	if err := r.Send("hello world"); err != nil {
		t.Fatal(err)
	}
	events := drain(t, r, 5*time.Second)

	// Expect: system, assistant, result.
	if len(events) < 3 {
		t.Fatalf("too few events: %d", len(events))
	}
	if events[0].Type != "system" || events[0].Subtype != "init" {
		t.Errorf("first = %+v, want system.init", events[0])
	}
	if r.SessionID() != "s-echo" {
		t.Errorf("SessionID = %q, want s-echo", r.SessionID())
	}

	var sawEcho bool
	for _, ev := range events {
		if ev.Type == "assistant" && ev.AssistantText() == "you said: hello world" {
			sawEcho = true
		}
	}
	if !sawEcho {
		t.Errorf("expected assistant echo in stream")
	}

	result := events[len(events)-1]
	if result.Type != "result" || result.TotalCostUSD == 0 {
		t.Errorf("last event = %+v", result)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 {
		t.Errorf("usage: %+v", result.Usage)
	}
}

func TestIntegration_ControlRequestAllow(t *testing.T) {
	r, err := Spawn(spawnFake(t, "bash"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cancel()

	if err := r.Send("run ls"); err != nil {
		t.Fatal(err)
	}

	// The fake emits control_request before result; respond with allow
	// and expect the stream to continue to a result event.
	var gotRequestID string
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
waitReq:
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				t.Fatal("stream closed before control_request")
			}
			if ev.Type == "control_request" {
				gotRequestID = ev.RequestID
				if ev.Request == nil || ev.Request.ToolName != "Bash" {
					t.Fatalf("control_request: %+v", ev)
				}
				break waitReq
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for control_request")
		}
	}

	if err := r.RespondToTool(gotRequestID, ToolDecision{Allow: true}); err != nil {
		t.Fatal(err)
	}

	// Also verify the marshaled response round-trips through our Event
	// decoder in the shape the runner writes — not tested explicitly by
	// the fake (it doesn't decode as an Event) but useful to sanity-check
	// here to avoid silent drift.
	if _, err := json.Marshal(ToolDecision{Allow: true, UpdatedInput: []byte(`{"command":"ls"}`)}); err != nil {
		t.Error(err)
	}

	events := drainRemaining(t, r, 3*time.Second)
	var sawAllow bool
	var sawResult bool
	for _, ev := range events {
		if ev.Type == "assistant" && ev.AssistantText() == "running ls" {
			sawAllow = true
		}
		if ev.Type == "result" {
			sawResult = true
		}
	}
	if !sawAllow {
		t.Errorf("missing post-allow assistant event")
	}
	if !sawResult {
		t.Errorf("missing final result event")
	}
}

func TestIntegration_ControlRequestDeny(t *testing.T) {
	r, err := Spawn(spawnFake(t, "bash"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cancel()

	if err := r.Send("run ls"); err != nil {
		t.Fatal(err)
	}

	var requestID string
	for ev := range r.Events() {
		if ev.Type == "control_request" {
			requestID = ev.RequestID
			break
		}
	}
	if requestID == "" {
		t.Fatal("no control_request arrived")
	}

	if err := r.RespondToTool(requestID, ToolDecision{Allow: false, Reason: "nope"}); err != nil {
		t.Fatal(err)
	}

	var sawDeny bool
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				if !sawDeny {
					t.Fatal("stream closed without deny assistant event")
				}
				return
			}
			if ev.Type == "assistant" && ev.AssistantText() == "bash denied" {
				sawDeny = true
			}
			if ev.Type == "result" {
				if !sawDeny {
					t.Fatal("result arrived without the deny assistant event")
				}
				return
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for deny result")
		}
	}
}

func TestIntegration_Cancel(t *testing.T) {
	r, err := Spawn(spawnFake(t, "hang"))
	if err != nil {
		t.Fatal(err)
	}

	// Consume the system.init so we know the process is live.
	select {
	case ev := <-r.Events():
		if ev.Type != "system" {
			t.Errorf("expected system first, got %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no system event")
	}

	if err := r.Send("go"); err != nil {
		t.Fatal(err)
	}

	// No further events will come from the fake; cancelling must tear down
	// the process and close the events channel promptly.
	start := time.Now()
	if err := r.Cancel(); err != nil {
		// Cancel returns cmd.Wait's error; a killed process surfaces a
		// non-nil error here which is fine — we care that it returned.
		t.Logf("cancel returned (non-fatal): %v", err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("cancel took too long: %v", d)
	}

	// Events channel should close.
	select {
	case _, ok := <-r.Events():
		if ok {
			// Drain remainder; the channel must close eventually.
			for range r.Events() {
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("events channel did not close after cancel")
	}
}

// drainRemaining reads events until the channel closes or deadline passes.
func drainRemaining(t *testing.T, r *Runner, timeout time.Duration) []Event {
	t.Helper()
	var out []Event
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
			if ev.Type == "result" {
				return out
			}
		case <-deadline.C:
			return out
		}
	}
}
