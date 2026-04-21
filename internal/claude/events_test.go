package claude

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, line string) Event {
	t.Helper()
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return ev
}

func TestDecode_SystemInit(t *testing.T) {
	ev := decode(t, `{"type":"system","subtype":"init","session_id":"s-1","model":"claude-x","cwd":"/repo"}`)
	if ev.Type != "system" || ev.Subtype != "init" {
		t.Fatalf("discriminator wrong: %+v", ev)
	}
	if ev.SessionID != "s-1" || ev.Model != "claude-x" || ev.CWD != "/repo" {
		t.Fatalf("fields: %+v", ev)
	}
}

func TestDecode_AssistantText(t *testing.T) {
	ev := decode(t, `{"type":"assistant","session_id":"s-1","message":{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"text","text":" world"}]}}`)
	if got := ev.AssistantText(); got != "hello world" {
		t.Fatalf("AssistantText = %q", got)
	}
}

func TestDecode_AssistantToolUse(t *testing.T) {
	ev := decode(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`)
	if len(ev.Message.Content) != 1 {
		t.Fatalf("content len: %d", len(ev.Message.Content))
	}
	b := ev.Message.Content[0]
	if b.Type != "tool_use" || b.Name != "Bash" || b.ID != "t1" {
		t.Fatalf("block: %+v", b)
	}
	if string(b.Input) != `{"command":"ls"}` {
		t.Fatalf("raw input: %s", string(b.Input))
	}
}

func TestDecode_UserToolResult(t *testing.T) {
	ev := decode(t, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok","is_error":false}]}}`)
	if len(ev.Message.Content) != 1 {
		t.Fatal("content len")
	}
	b := ev.Message.Content[0]
	if b.Type != "tool_result" || b.ToolUseID != "t1" || b.ResultIsError {
		t.Fatalf("block: %+v", b)
	}
}

func TestDecode_Result(t *testing.T) {
	ev := decode(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1200,"num_turns":3,"total_cost_usd":0.0123,"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":200,"cache_creation_input_tokens":10},"session_id":"s-1"}`)
	if ev.Type != "result" || ev.Subtype != "success" {
		t.Fatalf("discriminator: %+v", ev)
	}
	if ev.TotalCostUSD != 0.0123 || ev.NumTurns != 3 || ev.DurationMS != 1200 {
		t.Fatalf("summary fields: %+v", ev)
	}
	if ev.Usage == nil || ev.Usage.InputTokens != 100 || ev.Usage.OutputTokens != 50 {
		t.Fatalf("usage: %+v", ev.Usage)
	}
	if ev.Usage.CacheReadInputTokens != 200 || ev.Usage.CacheCreationInputTokens != 10 {
		t.Fatalf("cache tokens: %+v", ev.Usage)
	}
}

func TestDecode_ControlRequest(t *testing.T) {
	ev := decode(t, `{"type":"control_request","request_id":"req-7","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf node_modules"}}}`)
	if ev.Type != "control_request" || ev.RequestID != "req-7" {
		t.Fatalf("top-level: %+v", ev)
	}
	if ev.Request == nil || ev.Request.Subtype != "can_use_tool" || ev.Request.ToolName != "Bash" {
		t.Fatalf("request: %+v", ev.Request)
	}
	if string(ev.Request.Input) != `{"command":"rm -rf node_modules"}` {
		t.Fatalf("input: %s", string(ev.Request.Input))
	}
}

func TestAssistantText_IgnoresNonText(t *testing.T) {
	ev := decode(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}},{"type":"text","text":"hi"}]}}`)
	if got := ev.AssistantText(); got != "hi" {
		t.Fatalf("AssistantText = %q", got)
	}
}
