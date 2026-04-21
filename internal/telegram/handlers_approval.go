package telegram

import (
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// registerApproval wires the callback-query handler for Bash approval
// buttons. Callback data shape: "appr:<action>:<opID>".
func (r *Router) registerApproval(h *th.BotHandler) {
	h.Handle(r.handleApprovalCallback, th.CallbackDataPrefix(approvalPrefix))
}

func (r *Router) handleApprovalCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}

	action, opID, ok := parseApprovalCallback(cq.Data)
	if !ok {
		return r.answerCallback(ctx, cq.ID, "malformed approval")
	}

	decision, label, ok := decisionFromAction(action)
	if !ok {
		return r.answerCallback(ctx, cq.ID, "unknown action")
	}

	p, resolved := r.approvals.Resolve(opID, decision)
	if !resolved {
		// Pending expired (timeout), or was already resolved by another
		// button press. Silently acknowledge.
		return r.answerCallback(ctx, cq.ID, "expired")
	}

	_ = r.answerCallback(ctx, cq.ID, label)

	// Strip the keyboard and annotate the approval message so the history
	// shows what was decided. Plain text to sidestep MarkdownV2 escaping.
	if p.messageID != 0 {
		_, err := r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: p.chatID},
			MessageID: p.messageID,
			Text:      "$ " + truncate(p.command, 1000) + "\n\n" + label,
		})
		if err != nil {
			r.logger.Debug("edit approval message failed", "chat_id", p.chatID, "err", err)
		}
	}

	return nil
}

// parseApprovalCallback splits "appr:<action>:<opID>" into its pieces.
func parseApprovalCallback(data string) (action, opID string, ok bool) {
	if !strings.HasPrefix(data, approvalPrefix) {
		return "", "", false
	}
	rest := data[len(approvalPrefix):]
	i := strings.IndexByte(rest, ':')
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

func decisionFromAction(action string) (Decision, string, bool) {
	switch action {
	case actionAllow:
		return DecisionApprove, "✅ allowed", true
	case actionAllowSession:
		return DecisionApproveSession, "🔓 allowed for session", true
	case actionDeny:
		return DecisionDeny, "❌ denied", true
	case actionCancel:
		return DecisionCancelTurn, "🛑 turn cancelled", true
	}
	return "", "", false
}

func (r *Router) answerCallback(ctx *th.Context, queryID, text string) error {
	return r.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
		CallbackQueryID: queryID,
		Text:            text,
	})
}
