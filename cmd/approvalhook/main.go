// approvalhook is the PreToolUse hook binary invoked by the Claude Code CLI
// for every Bash tool use. It bridges the CLI's hook input/output protocol
// to the bot's approval server over a unix-domain socket.
//
// Wire protocol to the server: see internal/approval/proto.go.
//
// Wire protocol to the CLI: stdin carries a PreToolUse JSON payload
// (session_id, tool_name, tool_input, tool_use_id); stdout expects either
// an empty body (fall through to normal permission flow) or a hook-output
// JSON with permissionDecision=allow|deny. Exit code 0 for both paths.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/jancimertel/orb/internal/approval"
)

// Environment variables injected by the bot when it spawns the CLI.
const (
	envChatID = "CLAUDE_TG_BOT_CHAT_ID"
	envSocket = "CLAUDE_TG_BOT_APPROVAL_SOCKET"
)

type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type hookOutput struct {
	HookSpecificOutput hookSpecific `json:"hookSpecificOutput"`
}

type hookSpecific struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func main() {
	var in hookInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		// Can't read the hook input — fall through to the CLI's default
		// permission flow by emitting nothing.
		fmt.Fprintf(os.Stderr, "approvalhook: decode stdin: %v\n", err)
		os.Exit(0)
	}

	// We only gate Bash. The CLI matcher filters this already, but stay
	// defensive in case someone reuses the binary without a matcher.
	if in.ToolName != "Bash" {
		os.Exit(0)
	}

	chatID, err := strconv.ParseInt(os.Getenv(envChatID), 10, 64)
	if err != nil || chatID == 0 {
		writeDecision(approval.DecisionDeny, "approvalhook: "+envChatID+" not set")
		return
	}
	socketPath := os.Getenv(envSocket)
	if socketPath == "" {
		writeDecision(approval.DecisionDeny, "approvalhook: "+envSocket+" not set")
		return
	}

	command := extractCommand(in.ToolInput)

	resp, err := callServer(socketPath, approval.Request{
		ChatID:   chatID,
		ToolName: in.ToolName,
		Command:  command,
	})
	if err != nil {
		writeDecision(approval.DecisionDeny, "approval server unreachable: "+err.Error())
		return
	}

	writeDecision(resp.Decision, resp.Reason)
}

func callServer(socketPath string, req approval.Request) (approval.Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return approval.Response{}, err
	}
	defer conn.Close()

	// The operator may take a while to click; give them the full CLI hook
	// timeout minus a small margin.
	_ = conn.SetDeadline(time.Now().Add(14 * time.Minute))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return approval.Response{}, fmt.Errorf("encode request: %w", err)
	}

	var resp approval.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return approval.Response{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}

func writeDecision(decision, reason string) {
	out := hookOutput{
		HookSpecificOutput: hookSpecific{
			HookEventName:            "PreToolUse",
			PermissionDecision:       decision,
			PermissionDecisionReason: reason,
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

// extractCommand pulls the "command" field from the Bash tool_input JSON
// without fully unmarshalling into a schema.
func extractCommand(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m["command"].(string); ok {
		return v
	}
	return ""
}
