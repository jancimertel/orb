package telegram

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/session"
	"github.com/jancimertel/orb/internal/state"
)

// Callback data vocabulary: "sess:<action>[:<sessionID>]".
const (
	sessionPrefix    = "sess:"
	sessActionView   = "view"
	sessActionResume = "resume"
	sessActionDelete = "delete"
	sessActionDelOK  = "delok"
	sessActionClose  = "close"
)

// registerSession wires /new, /session and the sess:* callback handler.
func (r *Router) registerSession(h *th.BotHandler) {
	h.Handle(r.handleNew, th.CommandEqual("new"))
	h.Handle(r.handleSessionList, th.CommandEqual("session"))
	h.Handle(r.handleSessionCallback, th.CallbackDataPrefix(sessionPrefix))
}

// registerFallback attaches the catch-all text handler. Must be registered
// last — telego matches in registration order.
func (r *Router) registerFallback(h *th.BotHandler) {
	h.Handle(r.handleFallback, th.AnyMessageWithText())
}

// handleNew clears the chat's active session and tears down any live runner
// so the next prompt starts a fresh Claude session.
func (r *Router) handleNew(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	if err := r.store.SetActiveSession(ctx, chatID, ""); err != nil {
		r.logger.Error("clear session failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "failed to clear session — check logs")
	}
	r.registry.Reset(chatID)
	r.sessionAllow.Clear(chatID)
	return r.reply(ctx, chatID, "new session — send a prompt to begin")
}

func (r *Router) handleFallback(ctx *th.Context, u telego.Update) error {
	if u.Message == nil || u.Message.Text == "" {
		return nil
	}
	chatID := u.Message.Chat.ID

	// Users copy `/login` verbatim from Claude's "Not logged in" error, but
	// it's a CLI slash command — forwarding it to Claude just gets back
	// "/login isn't available in this environment." Short-circuit with the
	// actual re-auth procedure for this bot.
	if strings.EqualFold(strings.TrimSpace(u.Message.Text), "/login") {
		return r.reply(ctx, chatID,
			"/login is a Claude CLI command and can't be run from Telegram.\n\n"+
				"Re-auth from the host:\n"+
				"  docker compose exec -it bot claude login")
	}

	// Armed skill? Apply it once and clear. On a missing-skill race (user
	// reloaded between /skill arm and sending text), fall back to a plain
	// turn rather than dropping the message.
	if name := r.pendingSkills.Take(chatID); name != "" {
		if sk, err := r.lookupSkill(ctx, chatID, name); err == nil {
			return r.runSkill(ctx, chatID, sk, u.Message.Text)
		}
		_ = r.reply(ctx, chatID, "armed skill "+name+" no longer available — running plain turn")
	}
	return r.driveTurn(ctx, chatID, u.Message.Text)
}

// handleSessionList renders the /session picker. Optional numeric arg
// overrides the default page size (config MaxSessionsListed).
func (r *Router) handleSessionList(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID

	limit := r.cfg.MaxSessionsListed
	if arg := strings.TrimSpace(argAfterCommand(u.Message.Text)); arg != "" {
		if n, err := strconv.Atoi(arg); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	metas, err := r.sessions.List(limit)
	if err != nil {
		r.logger.Error("session list failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "failed to list sessions — check logs")
	}
	if len(metas) == 0 {
		return r.reply(ctx, chatID, "no sessions yet — send a prompt to create one")
	}

	repos, _ := r.store.ListRepos(ctx, chatID) // best-effort for alias lookup

	var rows [][]telego.InlineKeyboardButton
	for _, m := range metas {
		label := sessionButtonLabel(m, repos)
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label, CallbackData: sessionPrefix + sessActionView + ":" + m.ID},
		})
	}

	_, err = r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        fmt.Sprintf("Recent sessions (%d):", len(metas)),
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

// handleSessionCallback dispatches sess:* callback queries.
func (r *Router) handleSessionCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	action, sessionID, ok := parseSessionCallback(cq.Data)
	if !ok {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}

	chatID := callbackChatID(cq)

	switch action {
	case sessActionView:
		return r.sessionView(ctx, cq, chatID, sessionID)
	case sessActionResume:
		return r.sessionResume(ctx, cq, chatID, sessionID)
	case sessActionDelete:
		return r.sessionDeleteConfirm(ctx, cq, chatID, sessionID)
	case sessActionDelOK:
		return r.sessionDelete(ctx, cq, chatID, sessionID)
	case sessActionClose:
		return r.sessionClose(ctx, cq, chatID)
	}
	return r.answerCallback(ctx, cq.ID, "unknown action")
}

func (r *Router) sessionView(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, sessionID string) error {
	meta, err := r.sessions.Find(sessionID)
	if err != nil {
		return r.answerCallback(ctx, cq.ID, sessionLookupError(err))
	}
	_ = r.answerCallback(ctx, cq.ID, "")

	detail := formatSessionDetail(meta)
	kb := &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{
				{Text: "▶ Continue", CallbackData: sessionPrefix + sessActionResume + ":" + meta.ID},
				{Text: "🗑 Delete", CallbackData: sessionPrefix + sessActionDelete + ":" + meta.ID},
			},
			{
				{Text: "❌ Close", CallbackData: sessionPrefix + sessActionClose},
			},
		},
	}

	if cq.Message != nil {
		if _, err := r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:      telego.ChatID{ID: chatID},
			MessageID:   cq.Message.GetMessageID(),
			Text:        detail,
			ReplyMarkup: kb,
		}); err == nil {
			return nil
		}
	}
	_, err = r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        detail,
		ReplyMarkup: kb,
	})
	return err
}

func (r *Router) sessionResume(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, sessionID string) error {
	meta, err := r.sessions.Find(sessionID)
	if err != nil {
		return r.answerCallback(ctx, cq.ID, sessionLookupError(err))
	}
	if err := r.store.SetActiveSession(ctx, chatID, meta.ID); err != nil {
		r.logger.Error("resume: set session failed", "chat_id", chatID, "err", err)
		return r.answerCallback(ctx, cq.ID, "resume failed")
	}
	// Tear down any live runner so the next prompt respawns with --resume.
	r.registry.Reset(chatID)

	_ = r.answerCallback(ctx, cq.ID, "resumed")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      "▶ Resumed session " + meta.ID + "\n\nSend your next message.",
		})
	}
	return nil
}

func (r *Router) sessionDeleteConfirm(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, sessionID string) error {
	_ = r.answerCallback(ctx, cq.ID, "")
	kb := &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{
				{Text: "🗑 Really delete", CallbackData: sessionPrefix + sessActionDelOK + ":" + sessionID},
				{Text: "↩ Cancel", CallbackData: sessionPrefix + sessActionView + ":" + sessionID},
			},
		},
	}
	if cq.Message == nil {
		return nil
	}
	_, err := r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:      telego.ChatID{ID: chatID},
		MessageID:   cq.Message.GetMessageID(),
		Text:        "Delete session " + sessionID + "? This cannot be undone.",
		ReplyMarkup: kb,
	})
	return err
}

func (r *Router) sessionDelete(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, sessionID string) error {
	meta, err := r.sessions.Find(sessionID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r.answerCallback(ctx, cq.ID, sessionLookupError(err))
	}
	if err == nil {
		if err := session.Delete(meta.Path); err != nil {
			r.logger.Error("delete session failed", "chat_id", chatID, "path", meta.Path, "err", err)
			return r.answerCallback(ctx, cq.ID, "delete failed")
		}
	}

	// Clear active_session_id if it pointed at the deleted session.
	if cs, err := r.store.GetChatState(ctx, chatID); err == nil && cs.ActiveSessionID == sessionID {
		_ = r.store.SetActiveSession(ctx, chatID, "")
		r.registry.Reset(chatID)
	}

	_ = r.answerCallback(ctx, cq.ID, "deleted")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      "🗑 Deleted session " + sessionID,
		})
	}
	return nil
}

func (r *Router) sessionClose(ctx *th.Context, cq *telego.CallbackQuery, chatID int64) error {
	_ = r.answerCallback(ctx, cq.ID, "")
	if cq.Message == nil {
		return nil
	}
	_, err := r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: cq.Message.GetMessageID(),
		Text:      "Closed.",
	})
	return err
}

// --- helpers --------------------------------------------------------------

func parseSessionCallback(data string) (action, sessionID string, ok bool) {
	if !strings.HasPrefix(data, sessionPrefix) {
		return "", "", false
	}
	rest := data[len(sessionPrefix):]
	i := strings.IndexByte(rest, ':')
	if i < 0 {
		// Actions without an ID: "sess:close".
		return rest, "", true
	}
	return rest[:i], rest[i+1:], true
}

func callbackChatID(cq *telego.CallbackQuery) int64 {
	if cq.Message != nil {
		return cq.Message.GetChat().ID
	}
	return cq.From.ID
}

func sessionLookupError(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "session not found"
	}
	return "lookup failed"
}

// argAfterCommand returns the substring after the first space in a /command
// text. Returns "" when there's no argument.
func argAfterCommand(text string) string {
	i := strings.IndexByte(text, ' ')
	if i < 0 {
		return ""
	}
	return text[i+1:]
}

// sessionButtonLabel composes the row label for a session in the /session
// picker. Truncated to stay within Telegram's comfortable button width.
func sessionButtonLabel(m session.SessionMeta, repos []state.Repo) string {
	cwdLabel := aliasOrBaseCWD(m.CWD, repos)
	rel := relativeTime(m.LastActivityAt)
	summary := m.Summary
	if summary == "" {
		summary = fmt.Sprintf("%d msg", m.MessageCount)
	}
	label := fmt.Sprintf("🗂 %s · %s · %s", cwdLabel, rel, summary)
	return truncateRunes(label, 64)
}

func aliasOrBaseCWD(cwd string, repos []state.Repo) string {
	if cwd == "" {
		return "?"
	}
	for _, rp := range repos {
		if rp.Path != "" && rp.Path == cwd {
			return rp.Alias
		}
	}
	base := filepath.Base(cwd)
	if base == "" || base == "/" {
		return cwd
	}
	return base
}

func formatSessionDetail(m session.SessionMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s\n", m.ID)
	if m.CWD != "" {
		fmt.Fprintf(&b, "cwd:      %s\n", m.CWD)
	}
	if !m.StartedAt.IsZero() {
		fmt.Fprintf(&b, "started:  %s (%s)\n", m.StartedAt.Format("2006-01-02 15:04"), relativeTime(m.StartedAt))
	}
	if !m.LastActivityAt.IsZero() {
		fmt.Fprintf(&b, "last:     %s (%s)\n", m.LastActivityAt.Format("2006-01-02 15:04"), relativeTime(m.LastActivityAt))
	}
	fmt.Fprintf(&b, "messages: %d\n", m.MessageCount)
	fmt.Fprintf(&b, "size:     %s\n", humanBytes(m.SizeBytes))
	if m.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", m.Summary)
	}
	return b.String()
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "in the future"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// truncateRunes caps s at n runes, appending "…" when cut.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for idx := range s {
		if i >= n {
			return s[:idx] + "…"
		}
		i++
	}
	return s
}
