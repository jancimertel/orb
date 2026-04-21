package telegram

import (
	"context"
	"strconv"
	"time"

	"github.com/mymmrac/telego"

	"github.com/jancimertel/orb/internal/approval"
	"github.com/jancimertel/orb/internal/claude"
)

// Decide implements approval.Decider for the approval-server unix socket.
// It renders an inline keyboard in the operator's chat and blocks until a
// button is pressed, the timeout fires, or ctx is cancelled.
func (r *Router) Decide(ctx context.Context, req approval.Request) approval.Response {
	alog := r.logger.With(
		"chat_id", req.ChatID,
		"tool", req.ToolName,
		"command", truncate(req.Command, 200),
	)

	// Only Bash is gated. Anything else allows through so the matcher can
	// be widened later without a code change.
	if req.ToolName != "Bash" {
		return approval.Response{Decision: approval.DecisionAllow}
	}

	if reason, blocked := claude.HardDenyBash(req.Command); blocked {
		alog.Warn("bash approval auto-denied (hard-deny)", "reason", reason)
		tctx, cancel := r.tgCtx()
		_, _ = r.bot.SendMessage(tctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: req.ChatID},
			Text:   "🛑 Auto-denied Bash command:\n$ " + truncate(req.Command, 800) + "\n\nReason: " + reason,
		})
		cancel()
		return approval.Response{Decision: approval.DecisionDeny, Reason: "hard-deny: " + reason}
	}

	if r.sessionAllow.IsSet(req.ChatID) {
		alog.Info("bash auto-approved (session allow)")
		return approval.Response{Decision: approval.DecisionAllow}
	}

	alog.Info("bash approval requested")

	pending := r.approvals.Register(req.ChatID, req.Command)
	defer r.approvals.Forget(pending.opID)

	timeout := time.Duration(r.cfg.ApprovalTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	prompt := "🖥  Bash approval required:\n$ " + truncate(req.Command, 1000) +
		"\n\nAuto-deny in " + strconv.Itoa(int(timeout/time.Second)) + "s."
	tctx, cancel := r.tgCtx()
	msg, err := r.bot.SendMessage(tctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: req.ChatID},
		Text:        prompt,
		ReplyMarkup: approvalKeyboard(pending.opID),
	})
	cancel()
	if err != nil {
		alog.Error("send approval prompt failed", "err", err)
		return approval.Response{Decision: approval.DecisionDeny, Reason: "approval UI failed: " + err.Error()}
	}
	pending.setMessageID(msg.MessageID)

	var decision Decision
	waitStart := time.Now()
	select {
	case decision = <-pending.result:
	case <-time.After(timeout):
		decision = DecisionTimeout
	case <-ctx.Done():
		decision = DecisionCancelTurn
	}
	alog.Info("bash approval decided",
		"decision", string(decision),
		"waited_ms", time.Since(waitStart).Milliseconds(),
	)

	return r.finalize(req.ChatID, pending, req.Command, decision, timeout, alog)
}

// finalize performs any final UI edits and maps the internal Decision to the
// wire-level approval.Response.
func (r *Router) finalize(
	chatID int64,
	pending *pendingApproval,
	command string,
	decision Decision,
	timeout time.Duration,
	alog interface{ Warn(string, ...any) },
) approval.Response {
	switch decision {
	case DecisionApprove:
		return approval.Response{Decision: approval.DecisionAllow}
	case DecisionApproveSession:
		r.sessionAllow.Set(chatID)
		return approval.Response{Decision: approval.DecisionAllow}
	case DecisionDeny:
		return approval.Response{Decision: approval.DecisionDeny, Reason: "denied by operator"}
	case DecisionCancelTurn:
		r.sessionAllow.Clear(chatID)
		r.registry.Cancel(chatID)
		return approval.Response{Decision: approval.DecisionDeny, Reason: "operator cancelled turn"}
	case DecisionTimeout:
		if pending.messageID != 0 {
			tctx, cancel := r.tgCtx()
			_, err := r.bot.EditMessageText(tctx, &telego.EditMessageTextParams{
				ChatID:    telego.ChatID{ID: chatID},
				MessageID: pending.messageID,
				Text:      "$ " + truncate(command, 1000) + "\n\n⏱ timed out — auto-denied",
			})
			cancel()
			if err != nil {
				alog.Warn("edit timeout message failed", "err", err)
			}
		}
		return approval.Response{
			Decision: approval.DecisionDeny,
			Reason:   "no operator response in " + strconv.Itoa(int(timeout/time.Second)) + "s",
		}
	}
	return approval.Response{Decision: approval.DecisionDeny, Reason: "unknown internal decision"}
}

// tgCtx returns a short-lived context for bot API calls made outside of a
// telego handler (e.g. from the approval server goroutine). The caller is
// responsible for invoking the returned cancel. 30s is enough for a single
// SendMessage or EditMessageText round-trip.
func (r *Router) tgCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}
