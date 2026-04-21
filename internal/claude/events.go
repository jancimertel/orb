package claude

import "encoding/json"

// Event is a single stream-json line emitted by the Claude Code CLI, decoded
// to a discriminated union. Only the fields relevant to a given Type are
// populated — callers switch on Type before reading.
//
// Reference shape:
//   system.init     : {"type":"system","subtype":"init","session_id":"...","model":"...","cwd":"..."}
//   assistant       : {"type":"assistant","message":{"role":"assistant","content":[...],"model":"..."},"session_id":"..."}
//   user            : {"type":"user","message":{"role":"user","content":[...]},"session_id":"..."}   (tool_result echoes)
//   control_request : {"type":"control_request","request_id":"...","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{...}}}
//   result          : {"type":"result","subtype":"success","is_error":false,"total_cost_usd":...,"usage":{...},"session_id":"..."}
//
// NOTE: the exact on-the-wire shape for permission requests has shifted
// between Claude Code releases; fields here mirror the documented
// control_request schema. If a newer CLI emits a differently-typed
// permission event, add it to the union rather than reshaping this struct.
type Event struct {
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Model     string   `json:"model,omitempty"`
	CWD       string   `json:"cwd,omitempty"`
	Message   *Message `json:"message,omitempty"`

	// control_request fields
	RequestID string          `json:"request_id,omitempty"`
	Request   *ControlRequest `json:"request,omitempty"`

	// result fields
	IsError      bool    `json:"is_error,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
	Usage        *Usage  `json:"usage,omitempty"`
	ResultText   string  `json:"result,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// ControlRequest carries the payload of a control_request event. For a
// can_use_tool request, ToolName and Input are populated.
type ControlRequest struct {
	Subtype  string          `json:"subtype"` // e.g. "can_use_tool"
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
	Model   string  `json:"model,omitempty"`
}

// Block covers every content block type the CLI emits: text, tool_use,
// tool_result. Unused fields stay zero based on Type.
type Block struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID     string          `json:"tool_use_id,omitempty"`
	ResultContent json.RawMessage `json:"content,omitempty"`
	ResultIsError bool            `json:"is_error,omitempty"`
}

type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// AssistantText concatenates every text block from an assistant event. Returns
// empty string for non-assistant events.
func (e *Event) AssistantText() string {
	if e.Type != "assistant" || e.Message == nil {
		return ""
	}
	var out string
	for _, b := range e.Message.Content {
		if b.Type == "text" {
			out += b.Text
		}
	}
	return out
}
