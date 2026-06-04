package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/claude"
	"github.com/jancimertel/orb/internal/state"
)

// driveTurn runs a single Claude turn for chatID: spawn/reuse runner, send
// the prompt, stream events into a live-updating Telegram message via
// StreamRenderer, and finalize on the result event.
func (r *Router) driveTurn(ctx *th.Context, chatID int64, prompt string) error {
	cs, err := r.chatStateOrEmpty(ctx, chatID)
	if err != nil {
		r.logger.Error("chat state lookup failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "internal error — check logs")
	}

	cwd, err := r.resolveCWD(ctx, chatID, cs)
	if err != nil {
		return r.reply(ctx, chatID, fmt.Sprintf("repo %q not found — use /repo select", cs.ActiveRepoAlias))
	}

	model := cs.ActiveModel
	if model == "" {
		model = r.cfg.DefaultModel
	}

	agentName := r.activeAgentName(ctx, chatID)
	agentBody := r.resolveAgentBody(ctx, chatID)
	// MemoryPreamble wants the active repo root (empty → user-only). The
	// runner cwd would falsely match the scratch dir when no repo is
	// selected and try to read project memory from there.
	memoryBlock := r.agents.MemoryPreamble(r.activeRepoRoot(ctx, chatID))

	// The Claude Code CLI's --append-system-prompt is not reliably honored
	// with stream-json input (we saw silent drops in production), so we
	// inject the agent body (and memory block) as a user-turn preamble on
	// the FIRST turn of a fresh session. After that, it's part of session
	// history and applies automatically via --resume. /agent change clears
	// active_session_id, so the next turn is always a "fresh session" for
	// injection purposes. /remember tells the user to /new if they want
	// the new entry to take effect in the current conversation.
	preambleApplied := false
	memoryInjected := false
	if cs.ActiveSessionID == "" {
		var pre strings.Builder
		if agentBody != "" {
			fmt.Fprintf(&pre,
				"<system-instruction agent=%q>\n%s\n</system-instruction>\n\n",
				agentName, agentBody)
			preambleApplied = true
		}
		if memoryBlock != "" {
			pre.WriteString(memoryBlock)
			pre.WriteString("\n\n")
			memoryInjected = true
		}
		if pre.Len() > 0 {
			prompt = pre.String() + prompt
		}
	}

	turnID := newTurnID()
	tlog := r.logger.With(
		"chat_id", chatID,
		"turn_id", turnID,
		"session_id", cs.ActiveSessionID,
		"model", model,
		"effort", cs.ActiveEffort,
		"agent", cs.ActiveAgent,
		"agent_body_chars", len(agentBody),
		"agent_preamble_applied", preambleApplied,
		"memory_chars", len(memoryBlock),
		"memory_injected", memoryInjected,
	)

	turn, err := r.registry.StartTurn(chatID, claude.TurnOpts{
		CWD:                cwd,
		Model:              model,
		Effort:             cs.ActiveEffort,
		SessionID:          cs.ActiveSessionID,
		SystemPromptAppend: agentBody,
	})
	if err != nil {
		if errors.Is(err, claude.ErrBusy) {
			return r.reply(ctx, chatID, "busy — /cancel first")
		}
		tlog.Error("start turn failed", "err", err)
		return r.reply(ctx, chatID, "runner failed to start: "+err.Error())
	}
	defer turn.Release()

	stopTyping := r.startTypingHeartbeat(chatID)
	defer stopTyping()

	if err := turn.Runner().Send(prompt); err != nil {
		tlog.Error("send prompt failed", "err", err)
		r.registry.Cancel(chatID)
		return r.reply(ctx, chatID, "failed to send prompt — runner killed")
	}

	tlog.Info("turn started", "prompt_chars", len(prompt))

	sr := NewStreamRenderer(ctx, r.bot, chatID, r.logger)
	return r.consumeTurn(ctx, chatID, turn, sr, tlog)
}

// consumeTurn drains the runner's event channel and feeds the renderer.
// Returns when a result event arrives or the process exits.
func (r *Router) consumeTurn(ctx *th.Context, chatID int64, turn *claude.Turn, sr *StreamRenderer, tlog *slog.Logger) error {
	events := turn.Runner().Events()
	started := time.Now()

	for ev := range events {
		if r.handleEvent(ctx, chatID, turn, sr, ev, tlog, started) {
			return nil
		}
	}

	// Channel closed without a result — process exited (crash or cancel).
	<-turn.Runner().Done()

	fallback := "(failed)"
	if turn.Cancelled() {
		fallback = "(cancelled)"
	}
	_ = sr.Finalize(fallback)

	if turn.Cancelled() {
		tlog.Info("turn cancelled", "elapsed_ms", time.Since(started).Milliseconds())
		return nil
	}
	tail := turn.Runner().StderrTail()
	exitErr := turn.Runner().ExitError()
	tlog.Warn("runner exited before result",
		"exit_err", exitErr,
		"stderr_tail", tail,
		"elapsed_ms", time.Since(started).Milliseconds(),
	)
	errSummary := "exit"
	if exitErr != nil {
		errSummary = exitErr.Error()
	}
	if tail != "" {
		errSummary += " — " + truncate(tail, 200)
	}
	r.status.RecordError(chatID, "runner", errSummary)

	msg := "runner exited unexpectedly"
	if tail != "" {
		msg += "\n\nstderr:\n" + truncate(tail, 800)
	}
	return r.reply(ctx, chatID, msg)
}

// handleEvent dispatches a single event to the renderer and (on result) does
// the post-turn bookkeeping. Returns true when the turn is done.
func (r *Router) handleEvent(ctx *th.Context, chatID int64, turn *claude.Turn, sr *StreamRenderer, ev claude.Event, tlog *slog.Logger, started time.Time) bool {
	switch ev.Type {
	case "system":
		// session_id captured inside Runner; nothing user-facing.
	case "assistant":
		if ev.Message == nil {
			return false
		}
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "text":
				sr.AppendText(b.Text)
			case "tool_use":
				sr.AppendTool(toolEntryFromBlock(b))
			}
		}
	case "user":
		// tool_result echoes from Claude — not shown to user.
	case "result":
		final := sr.Finalize("(no response)")
		if sid := turn.Runner().SessionID(); sid != "" {
			if err := r.store.SetActiveSession(ctx, chatID, sid); err != nil {
				tlog.Warn("persist session id failed", "err", err)
			}
		}
		if err := r.usage.RecordResult(ctx, chatID, &ev); err != nil {
			tlog.Warn("record usage failed", "err", err)
		}
		if ev.IsError {
			detail := strings.TrimSpace(ev.ResultText)
			tail := strings.TrimSpace(turn.Runner().StderrTail())
			tlog.Warn("turn errored",
				"subtype", ev.Subtype,
				"result", truncate(detail, 1000),
				"stderr_tail", truncate(tail, 1500),
				"elapsed_ms", time.Since(started).Milliseconds(),
			)
			summary := detail
			if summary == "" {
				summary = "subtype=" + ev.Subtype
			}
			r.status.RecordError(chatID, "claude", truncate(summary, 400))

			body := "⚠ Claude turn errored (subtype=" + ev.Subtype + ")"
			if detail != "" {
				body += "\n\n" + truncate(detail, 1200)
			}
			if tail != "" {
				body += "\n\nstderr:\n" + truncate(tail, 1500)
			}
			// The Claude CLI tells the user to run `/login`, but inside this
			// bot that's a CLI instruction, not a Telegram command — the
			// operator has to re-auth from a shell in the container.
			lowered := strings.ToLower(detail)
			if strings.Contains(lowered, "not logged in") ||
				strings.Contains(lowered, "/login isn't available") {
				body += "\n\nRe-auth from the host:\n" +
					"  docker compose exec -it bot claude login"
			}
			_ = r.reply(ctx, chatID, body)
			return true
		}
		tlog.Info("turn completed",
			"cost_usd", ev.TotalCostUSD,
			"num_turns", ev.NumTurns,
			"chars_out", len(final),
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
		return true
	case "__decode_error":
		tlog.Warn("stream-json decode error", "err", ev.ResultText)
	default:
		tlog.Debug("unhandled stream event", "type", ev.Type)
	}
	return false
}

// startTypingHeartbeat fires a "typing" chat action immediately and refreshes
// it every 4s until the returned cancel func is called. Telegram clears the
// indicator ~5s after the last action, so 4s keeps it steady. Errors are
// logged at debug — a failed heartbeat should never break a turn.
func (r *Router) startTypingHeartbeat(chatID int64) func() {
	ctx, cancel := context.WithCancel(context.Background())
	send := func() {
		if err := r.bot.SendChatAction(ctx, &telego.SendChatActionParams{
			ChatID: telego.ChatID{ID: chatID},
			Action: "typing",
		}); err != nil && ctx.Err() == nil {
			r.logger.Warn("send chat action failed", "chat_id", chatID, "err", err)
		}
	}
	send()
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				send()
			}
		}
	}()
	return cancel
}

// newTurnID returns a short random identifier to correlate log lines within
// a single turn. Not security-sensitive; 8 hex chars is plenty for humans
// to grep.
func newTurnID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// resolveCWD returns the working directory the runner should start in.
func (r *Router) resolveCWD(ctx context.Context, chatID int64, cs *state.ChatState) (string, error) {
	if cs.ActiveRepoAlias == "" {
		return r.cfg.ScratchDir(), nil
	}
	repo, err := r.store.GetRepo(ctx, chatID, cs.ActiveRepoAlias)
	if err != nil {
		return "", err
	}
	return repo.Path, nil
}

// toolEntryFromBlock extracts the structured tool metadata the renderer
// needs to group and format tool runs. Unknown tools still get recorded (by
// name) so the user can see them in the log.
func toolEntryFromBlock(b claude.Block) toolEntry {
	e := toolEntry{name: b.Name}
	switch b.Name {
	case "Bash":
		e.cmd = extractField(b.Input, "command")
	case "Edit", "Write", "MultiEdit", "Read", "NotebookEdit":
		e.path = extractPath(b.Input)
	}
	return e
}

// extractPath pulls a "file_path" value from a tool input JSON without a
// full schema.
func extractPath(raw []byte) string {
	return extractField(raw, "file_path")
}

// extractField does a best-effort string lookup for a top-level JSON string
// field. Returns empty string if not found.
func extractField(raw []byte, key string) string {
	needle := `"` + key + `":"`
	s := string(raw)
	i := strings.Index(s, needle)
	if i < 0 {
		return ""
	}
	s = s[i+len(needle):]
	j := strings.IndexByte(s, '"')
	if j < 0 {
		return ""
	}
	return s[:j]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// chunkMessage splits s into pieces <= limit chars, preferring newline
// boundaries. Never returns an empty slice.
func chunkMessage(s string, limit int) []string {
	if len(s) <= limit {
		return []string{s}
	}
	var out []string
	for len(s) > limit {
		cut := limit
		if idx := strings.LastIndex(s[:limit], "\n"); idx > limit/2 {
			cut = idx + 1
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}
