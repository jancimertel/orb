package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// TestMain re-exec's the test binary as fake-claude when CLAUDE_FAKE=1 is
// set. The integration tests spawn the test binary itself (os.Args[0]) as
// the "claude" CLI, with ExtraEnv setting CLAUDE_FAKE=1 and
// CLAUDE_FAKE_SCENARIO=<name> so a single binary can emulate multiple CLI
// behaviors without shipping a separate fake-claude build step.
func TestMain(m *testing.M) {
	if os.Getenv("CLAUDE_FAKE") == "1" {
		if err := runFakeCLI(); err != nil {
			fmt.Fprintln(os.Stderr, "fake-claude:", err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFakeCLI is a tiny stand-in for the real Claude Code CLI. It reads
// NDJSON from stdin and writes scripted NDJSON to stdout. Scenario is
// selected via CLAUDE_FAKE_SCENARIO; each scenario documents the exact
// sequence it drives.
func runFakeCLI() error {
	scenario := os.Getenv("CLAUDE_FAKE_SCENARIO")
	sessionID := os.Getenv("CLAUDE_FAKE_SESSION")
	if sessionID == "" {
		sessionID = "fake-session"
	}

	// Always open with a system.init so the runner captures session_id.
	if err := emit(map[string]any{
		"type":       "system",
		"subtype":    "init",
		"session_id": sessionID,
		"model":      "fake-model",
		"cwd":        ".",
	}); err != nil {
		return err
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<16), 1<<20)

	for sc.Scan() {
		var in map[string]any
		if err := json.Unmarshal(sc.Bytes(), &in); err != nil {
			continue
		}
		kind, _ := in["type"].(string)

		switch {
		case kind == "user" && scenario == "echo":
			text := extractUserText(in)
			_ = emit(assistantText("you said: " + text))
			_ = emit(resultOK(sessionID, 0.001))

		case kind == "user" && scenario == "bash":
			// First ask for permission to run a benign Bash command, then wait
			// for the caller's control_response before emitting the result.
			_ = emit(map[string]any{
				"type":       "control_request",
				"request_id": "fake-req-1",
				"request": map[string]any{
					"subtype":   "can_use_tool",
					"tool_name": "Bash",
					"input":     map[string]any{"command": "ls -la"},
				},
			})

		case kind == "control_response" && scenario == "bash":
			behavior := controlResponseBehavior(in)
			switch behavior {
			case "allow":
				_ = emit(assistantText("running ls"))
				_ = emit(resultOK(sessionID, 0.002))
			case "deny":
				_ = emit(assistantText("bash denied"))
				_ = emit(resultOK(sessionID, 0.0))
			default:
				_ = emit(assistantText("unknown behavior"))
				_ = emit(resultOK(sessionID, 0.0))
			}

		case kind == "user" && scenario == "hang":
			// Never respond — used for cancel tests. The caller kills us.
			continue
		}
	}
	return nil
}

// --- helpers --------------------------------------------------------------

func emit(v map[string]any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = os.Stdout.Write(b)
	return err
}

func assistantText(text string) map[string]any {
	return map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	}
}

func resultOK(session string, costUSD float64) map[string]any {
	return map[string]any{
		"type":           "result",
		"subtype":        "success",
		"is_error":       false,
		"session_id":     session,
		"total_cost_usd": costUSD,
		"num_turns":      1,
		"usage": map[string]any{
			"input_tokens":                10,
			"output_tokens":               5,
			"cache_read_input_tokens":     0,
			"cache_creation_input_tokens": 0,
		},
	}
}

func extractUserText(msg map[string]any) string {
	m, _ := msg["message"].(map[string]any)
	if m == nil {
		return ""
	}
	content, _ := m["content"].([]any)
	for _, c := range content {
		b, _ := c.(map[string]any)
		if b == nil {
			continue
		}
		if t, _ := b["type"].(string); t == "text" {
			if s, _ := b["text"].(string); s != "" {
				return s
			}
		}
	}
	return ""
}

// controlResponseBehavior pulls ev.response.response.behavior out of the
// nested control_response shape.
func controlResponseBehavior(ev map[string]any) string {
	outer, _ := ev["response"].(map[string]any)
	if outer == nil {
		return ""
	}
	inner, _ := outer["response"].(map[string]any)
	if inner == nil {
		return ""
	}
	b, _ := inner["behavior"].(string)
	return b
}
