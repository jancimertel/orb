package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// SpawnOpts configures a freshly-spawned Claude CLI process.
type SpawnOpts struct {
	CLIPath   string // default "claude"
	CWD       string // working directory for the process; must exist
	HomeDir   string // HOME env for the subprocess (e.g. /data)
	APIKey    string // ANTHROPIC_API_KEY
	Model     string // optional; maps to --model
	SessionID string // optional; maps to --resume

	// HookBinary is the absolute path to the approvalhook binary invoked
	// by the CLI for Bash tool uses. Empty disables the hook (Bash calls
	// will follow the --permission-mode default behaviour).
	HookBinary string

	// ApprovalSocket is the unix-domain socket path the hook dials to
	// reach the bot's approval server. Passed to the hook via env.
	ApprovalSocket string

	// ChatID is the Telegram chat that owns this CLI process. Passed to
	// the hook via env so it can address the right operator.
	ChatID int64

	// ExtraEnv is appended to the child's environment verbatim. Used by
	// integration tests to route the spawned binary through a fake-claude
	// codepath without leaking that switch to production callers.
	ExtraEnv []string

	// SystemPromptAppend is passed to the CLI via --append-system-prompt.
	// Empty = omit. The bot uses this to inject the active agent body so
	// the agent's voice/style sticks for every turn in the session without
	// changing the CLI's built-in system prompt.
	SystemPromptAppend string
}

// Runner owns one Claude CLI subprocess. It is safe for concurrent use across
// the goroutines that read events and the one that sends turns, but callers
// must serialize Send() invocations per Runner (the Registry enforces this).
type Runner struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan Event
	done   chan struct{}

	stderrBuf *ringBuffer

	mu        sync.Mutex
	sessionID string
	exitErr   error
}

// Spawn starts the Claude CLI as a subprocess wired for NDJSON stream-json
// input/output. It does not send a prompt; call Send for that.
//
// The subprocess runs detached from the caller's context so that it outlives
// individual Telegram request contexts; tear it down explicitly via Cancel or
// Close.
func Spawn(opts SpawnOpts) (*Runner, error) {
	cli := opts.CLIPath
	if cli == "" {
		cli = "claude"
	}

	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-mode", "acceptEdits",
	}
	if opts.HookBinary != "" {
		settings, err := buildHookSettings(opts.HookBinary)
		if err != nil {
			return nil, err
		}
		args = append(args, "--settings", settings)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPromptAppend != "" {
		args = append(args, "--append-system-prompt", opts.SystemPromptAppend)
	}

	cmd := exec.Command(cli, args...)
	cmd.Dir = opts.CWD
	cmd.Env = buildEnv(opts)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderrBuf := newRingBuffer(8192)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: start: %w", err)
	}

	r := &Runner{
		cmd:       cmd,
		stdin:     stdin,
		events:    make(chan Event, 32),
		done:      make(chan struct{}),
		stderrBuf: stderrBuf,
	}

	go r.readLoop(stdout)
	go r.waitLoop()

	return r, nil
}

func buildEnv(opts SpawnOpts) []string {
	env := os.Environ()
	if opts.HomeDir != "" {
		env = append(env, "HOME="+opts.HomeDir)
	}
	if opts.APIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+opts.APIKey)
	}
	if opts.ChatID != 0 {
		env = append(env, fmt.Sprintf("CLAUDE_TG_BOT_CHAT_ID=%d", opts.ChatID))
	}
	if opts.ApprovalSocket != "" {
		env = append(env, "CLAUDE_TG_BOT_APPROVAL_SOCKET="+opts.ApprovalSocket)
	}
	env = append(env, opts.ExtraEnv...)
	return env
}

// buildHookSettings returns a JSON string suitable for --settings that wires
// the PreToolUse hook for the Bash tool to hookBinary. Timeout is generous
// so the operator has time to click; the bot's approval server enforces a
// shorter internal deadline on top.
func buildHookSettings(hookBinary string) (string, error) {
	payload := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Bash",
					"hooks": []map[string]any{
						{
							"type":    "command",
							"command": hookBinary,
							"timeout": 900,
						},
					},
				},
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("claude: marshal hook settings: %w", err)
	}
	return string(b), nil
}

// Events returns the channel carrying decoded stream-json events. The channel
// is closed when the subprocess stdout reaches EOF (usually right before the
// process exits).
func (r *Runner) Events() <-chan Event { return r.events }

// Done is closed when the subprocess has exited.
func (r *Runner) Done() <-chan struct{} { return r.done }

// Alive reports whether the subprocess is still running.
func (r *Runner) Alive() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// SessionID returns the session identifier captured from the first system.init
// event, or empty if not yet received.
func (r *Runner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID
}

// StderrTail returns the last N bytes written to stderr (capped at the ring
// buffer size). Useful for reporting crashes to the user.
func (r *Runner) StderrTail() string {
	return r.stderrBuf.String()
}

// ExitError returns the error returned by cmd.Wait, or nil if the process is
// still running / exited cleanly.
func (r *Runner) ExitError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitErr
}

// Send writes a single user turn to the subprocess stdin as an NDJSON line.
// Callers must serialize Send calls (one turn at a time).
func (r *Runner) Send(text string) error {
	payload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	}
	return r.writeJSONLine(payload, "marshal user turn")
}

// ToolDecision is the answer to a can_use_tool control request.
type ToolDecision struct {
	Allow        bool
	Reason       string          // surfaced to Claude when Allow=false
	UpdatedInput json.RawMessage // optional; nil passes the original input through unchanged
}

// RespondToTool writes a control_response line to the subprocess stdin in
// reply to a can_use_tool control_request. Callers must invoke this exactly
// once per request.
//
// The message shape mirrors the documented Claude Code stream-json control
// protocol. If a CLI release changes it, update this method in one place.
func (r *Runner) RespondToTool(requestID string, d ToolDecision) error {
	var inner map[string]any
	if d.Allow {
		inner = map[string]any{"behavior": "allow"}
		if d.UpdatedInput != nil {
			inner["updatedInput"] = json.RawMessage(d.UpdatedInput)
		}
	} else {
		reason := d.Reason
		if reason == "" {
			reason = "denied by operator"
		}
		inner = map[string]any{
			"behavior": "deny",
			"message":  reason,
		}
	}
	payload := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   inner,
		},
	}
	return r.writeJSONLine(payload, "marshal control response")
}

// writeJSONLine marshals payload and writes it followed by a newline.
func (r *Runner) writeJSONLine(payload any, errCtx string) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("claude: %s: %w", errCtx, err)
	}
	b = append(b, '\n')
	if _, err := r.stdin.Write(b); err != nil {
		return fmt.Errorf("claude: write stdin: %w", err)
	}
	return nil
}

// Cancel terminates the subprocess: SIGTERM, then SIGKILL after a short grace
// window. It blocks until the process has exited.
func (r *Runner) Cancel() error {
	if r.cmd.Process == nil {
		return nil
	}
	_ = r.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		_ = r.cmd.Process.Kill()
		<-r.done
	}
	return r.ExitError()
}

// Close closes stdin (signaling clean shutdown) and waits for the process to
// exit. Use when the chat wants to end the session cleanly; use Cancel for an
// abrupt interrupt.
func (r *Runner) Close() error {
	_ = r.stdin.Close()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		return r.Cancel()
	}
	return r.ExitError()
}

// readLoop decodes NDJSON lines from stdout and pushes Event values on the
// events channel. Closes the channel when stdout EOFs.
func (r *Runner) readLoop(stdout io.Reader) {
	defer close(r.events)

	sc := bufio.NewScanner(stdout)
	// Claude responses can include large tool_result blobs; bump the max line.
	sc.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}

		var ev Event
		if err := json.Unmarshal(trimmed, &ev); err != nil {
			// Unknown/malformed line — surface via a synthetic event so the
			// caller can log without blocking the pipe.
			r.events <- Event{Type: "__decode_error", ResultText: err.Error()}
			continue
		}
		ev.Raw = append([]byte(nil), trimmed...)

		if ev.Type == "system" && ev.Subtype == "init" && ev.SessionID != "" {
			r.mu.Lock()
			if r.sessionID == "" {
				r.sessionID = ev.SessionID
			}
			r.mu.Unlock()
		}

		r.events <- ev
	}
}

// waitLoop reaps the subprocess and closes r.done.
func (r *Runner) waitLoop() {
	err := r.cmd.Wait()
	r.mu.Lock()
	r.exitErr = err
	r.mu.Unlock()
	close(r.done)
}

// --- stderr ring buffer ---------------------------------------------------

// ringBuffer keeps the last N bytes written to it. Writes never block or fail.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
	full bool
	pos  int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, 0, size), size: size}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	if !r.full && len(r.buf)+n <= r.size {
		r.buf = append(r.buf, p...)
		return n, nil
	}
	// Switch to fixed-size ring; expand once to full capacity.
	if !r.full {
		tmp := make([]byte, r.size)
		copy(tmp, r.buf)
		r.pos = len(r.buf)
		r.buf = tmp
		r.full = true
	}
	// Write bytes into the ring, wrapping as needed.
	if n >= r.size {
		copy(r.buf, p[n-r.size:])
		r.pos = 0
		return n, nil
	}
	first := r.size - r.pos
	if first > n {
		first = n
	}
	copy(r.buf[r.pos:], p[:first])
	if n > first {
		copy(r.buf, p[first:])
	}
	r.pos = (r.pos + n) % r.size
	return n, nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return string(r.buf)
	}
	out := make([]byte, 0, r.size)
	out = append(out, r.buf[r.pos:]...)
	out = append(out, r.buf[:r.pos]...)
	return string(out)
}

// --- errors ---------------------------------------------------------------

// ErrBusy is returned by Registry.StartTurn when a turn is already in flight
// for the chat.
var ErrBusy = errors.New("claude: runner is busy")
