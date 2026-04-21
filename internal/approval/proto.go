// Package approval implements the bridge between the Claude Code CLI's
// PreToolUse hook and the bot's Telegram approval UI.
//
// Wire shape: the hook binary dials a unix-domain socket, writes a single
// line of JSON describing the tool use, then reads a single line of JSON
// describing the operator's decision. Each hook invocation uses its own
// connection.
package approval

// Request is the payload the hook sends to the server.
type Request struct {
	ChatID   int64  `json:"chat_id"`
	ToolName string `json:"tool_name"`
	Command  string `json:"command"`
}

// Response is the decision the server returns to the hook.
//
// Decision is one of "allow" or "deny". Reason is surfaced to Claude when
// denying and shown in logs; it may be empty on allow.
type Response struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)
