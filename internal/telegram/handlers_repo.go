package telegram

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/repo"
	"github.com/jancimertel/orb/internal/state"
)

// Callback data vocabulary: "repo:<action>:<alias>".
const (
	repoPrefix         = "repo:"
	repoActionSelect   = "select"
	repoActionView     = "view"
	repoActionRmAsk    = "rmask"
	repoActionRmOK     = "rmok"
	repoActionClose    = "close"
)

// registerRepo wires /repo, /cd, /pwd plus the repo:* callback handler.
func (r *Router) registerRepo(h *th.BotHandler) {
	h.Handle(r.handleRepo, th.CommandEqual("repo"))
	h.Handle(r.handleCD, th.CommandEqual("cd"))
	h.Handle(r.handlePWD, th.CommandEqual("pwd"))
	h.Handle(r.handleRepoCallback, th.CallbackDataPrefix(repoPrefix))
}

func (r *Router) handleRepo(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	args := argAfterCommand(u.Message.Text)
	sub, rest := splitFirstWord(args)

	switch strings.ToLower(sub) {
	case "", "list":
		return r.repoList(ctx, chatID)
	case "add":
		return r.repoAdd(ctx, chatID, rest)
	case "select":
		alias := strings.TrimSpace(rest)
		if alias == "" {
			return r.reply(ctx, chatID, "usage: /repo select <alias>")
		}
		return r.repoSelect(ctx, chatID, alias)
	case "remove", "rm":
		alias := strings.TrimSpace(rest)
		if alias == "" {
			return r.reply(ctx, chatID, "usage: /repo remove <alias>")
		}
		return r.repoRemoveAsk(ctx, chatID, alias)
	default:
		return r.reply(ctx, chatID,
			"usage:\n"+
				"  /repo                 list\n"+
				"  /repo add <url> [a]   clone\n"+
				"  /repo select <a>      set active\n"+
				"  /repo remove <a>      confirm + delete")
	}
}

func (r *Router) handleCD(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	alias := strings.TrimSpace(argAfterCommand(u.Message.Text))
	if alias == "" {
		return r.reply(ctx, u.Message.Chat.ID, "usage: /cd <alias>")
	}
	return r.repoSelect(ctx, u.Message.Chat.ID, alias)
}

func (r *Router) handlePWD(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	cs, err := r.chatStateOrEmpty(ctx, chatID)
	if err != nil {
		r.logger.Error("pwd: chat state failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "internal error — check logs")
	}
	if cs.ActiveRepoAlias == "" {
		return r.reply(ctx, chatID, "no active repo — /repo select <alias>")
	}
	rec, err := r.repos.Get(ctx, chatID, cs.ActiveRepoAlias)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return r.reply(ctx, chatID, fmt.Sprintf("active alias %q missing from registry — /repo list", cs.ActiveRepoAlias))
		}
		r.logger.Error("pwd: repo lookup failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "lookup failed — check logs")
	}
	return r.reply(ctx, chatID, formatRepoPWD(rec))
}

// --- /repo subcommand handlers --------------------------------------------

func (r *Router) repoAdd(ctx *th.Context, chatID int64, rest string) error {
	url, alias := splitFirstWord(rest)
	if url == "" {
		return r.reply(ctx, chatID, "usage: /repo add <url> [alias]")
	}
	_ = r.reply(ctx, chatID, "cloning… (this may take a moment)")

	rec, err := r.repos.Add(ctx, repo.AddOpts{
		ChatID: chatID,
		URL:    url,
		Alias:  strings.TrimSpace(alias),
	})
	if err != nil {
		r.logger.Warn("repo add failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "add failed: "+truncate(err.Error(), 1200))
	}
	return r.reply(ctx, chatID,
		fmt.Sprintf("✅ cloned\nalias:  %s\nurl:    %s\nbranch: %s\npath:   %s",
			rec.Alias, rec.URL, firstNonEmpty(rec.DefaultBranch, "?"), rec.Path))
}

func (r *Router) repoList(ctx *th.Context, chatID int64) error {
	repos, err := r.repos.List(ctx, chatID)
	if err != nil {
		r.logger.Error("repo list failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "list failed — check logs")
	}
	if len(repos) == 0 {
		return r.reply(ctx, chatID, "no repos registered — /repo add <url>")
	}
	cs, _ := r.chatStateOrEmpty(ctx, chatID)
	active := cs.ActiveRepoAlias

	var rows [][]telego.InlineKeyboardButton
	for _, rp := range repos {
		marker := "  "
		if rp.Alias == active {
			marker = "✓ "
		}
		label := fmt.Sprintf("%s%s · %s", marker, rp.Alias, firstNonEmpty(rp.CurrentBranch, "?"))
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: truncateRunes(label, 64), CallbackData: repoPrefix + repoActionView + ":" + rp.Alias},
		})
	}
	_, err = r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        fmt.Sprintf("Registered repos (%d):", len(repos)),
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) repoSelect(ctx *th.Context, chatID int64, alias string) error {
	rec, err := r.repos.Select(ctx, chatID, alias)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return r.reply(ctx, chatID, fmt.Sprintf("alias %q not registered", alias))
		}
		r.logger.Error("repo select failed", "chat_id", chatID, "alias", alias, "err", err)
		return r.reply(ctx, chatID, "select failed — check logs")
	}
	// Tear down the current runner so the next prompt runs in the new cwd.
	r.registry.Reset(chatID)
	return r.reply(ctx, chatID, fmt.Sprintf("📂 active repo: %s\npath: %s", rec.Alias, rec.Path))
}

func (r *Router) repoRemoveAsk(ctx *th.Context, chatID int64, alias string) error {
	if _, err := r.store.GetRepo(ctx, chatID, alias); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return r.reply(ctx, chatID, fmt.Sprintf("alias %q not registered", alias))
		}
		return r.reply(ctx, chatID, "lookup failed — check logs")
	}
	kb := &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{
				{Text: "🗑 Really remove", CallbackData: repoPrefix + repoActionRmOK + ":" + alias},
				{Text: "↩ Cancel", CallbackData: repoPrefix + repoActionClose},
			},
		},
	}
	_, err := r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        fmt.Sprintf("Remove %q? This deletes the cloned working tree.", alias),
		ReplyMarkup: kb,
	})
	return err
}

// --- callback dispatch ----------------------------------------------------

func (r *Router) handleRepoCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	action, alias, ok := parseRepoCallback(cq.Data)
	if !ok {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	switch action {
	case repoActionView:
		return r.repoCallbackView(ctx, cq, chatID, alias)
	case repoActionSelect:
		return r.repoCallbackSelect(ctx, cq, chatID, alias)
	case repoActionRmAsk:
		return r.repoCallbackRmAsk(ctx, cq, chatID, alias)
	case repoActionRmOK:
		return r.repoCallbackRmOK(ctx, cq, chatID, alias)
	case repoActionClose:
		return r.repoCallbackClose(ctx, cq, chatID)
	}
	return r.answerCallback(ctx, cq.ID, "unknown action")
}

func (r *Router) repoCallbackView(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, alias string) error {
	rec, err := r.repos.Get(ctx, chatID, alias)
	if err != nil {
		return r.answerCallback(ctx, cq.ID, "not found")
	}
	_ = r.answerCallback(ctx, cq.ID, "")
	kb := &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{
				{Text: "📂 Select", CallbackData: repoPrefix + repoActionSelect + ":" + alias},
				{Text: "🗑 Remove", CallbackData: repoPrefix + repoActionRmAsk + ":" + alias},
			},
			{
				{Text: "❌ Close", CallbackData: repoPrefix + repoActionClose},
			},
		},
	}
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:      telego.ChatID{ID: chatID},
			MessageID:   cq.Message.GetMessageID(),
			Text:        formatRepoPWD(rec),
			ReplyMarkup: kb,
		})
	}
	return nil
}

func (r *Router) repoCallbackSelect(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, alias string) error {
	rec, err := r.repos.Select(ctx, chatID, alias)
	if err != nil {
		return r.answerCallback(ctx, cq.ID, "select failed")
	}
	r.registry.Reset(chatID)
	_ = r.answerCallback(ctx, cq.ID, "selected")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      fmt.Sprintf("📂 active repo: %s\npath: %s", rec.Alias, rec.Path),
		})
	}
	return nil
}

func (r *Router) repoCallbackRmAsk(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, alias string) error {
	_ = r.answerCallback(ctx, cq.ID, "")
	if cq.Message == nil {
		return nil
	}
	_, err := r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: cq.Message.GetMessageID(),
		Text:      fmt.Sprintf("Remove %q? This deletes the cloned working tree.", alias),
		ReplyMarkup: &telego.InlineKeyboardMarkup{
			InlineKeyboard: [][]telego.InlineKeyboardButton{
				{
					{Text: "🗑 Really remove", CallbackData: repoPrefix + repoActionRmOK + ":" + alias},
					{Text: "↩ Cancel", CallbackData: repoPrefix + repoActionView + ":" + alias},
				},
			},
		},
	})
	return err
}

func (r *Router) repoCallbackRmOK(ctx *th.Context, cq *telego.CallbackQuery, chatID int64, alias string) error {
	if err := r.repos.Remove(ctx, chatID, alias); err != nil {
		r.logger.Error("repo remove failed", "chat_id", chatID, "alias", alias, "err", err)
		return r.answerCallback(ctx, cq.ID, "remove failed")
	}
	// If the removed alias was the active one, clear and reset the runner.
	if cs, err := r.store.GetChatState(ctx, chatID); err == nil && cs.ActiveRepoAlias == alias {
		_ = r.store.SetActiveRepo(ctx, chatID, "")
		r.registry.Reset(chatID)
	}
	_ = r.answerCallback(ctx, cq.ID, "removed")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      fmt.Sprintf("🗑 removed %q", alias),
		})
	}
	return nil
}

func (r *Router) repoCallbackClose(ctx *th.Context, cq *telego.CallbackQuery, chatID int64) error {
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

func parseRepoCallback(data string) (action, alias string, ok bool) {
	if !strings.HasPrefix(data, repoPrefix) {
		return "", "", false
	}
	rest := data[len(repoPrefix):]
	i := strings.IndexByte(rest, ':')
	if i < 0 {
		return rest, "", true
	}
	return rest[:i], rest[i+1:], true
}

func splitFirstWord(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

func formatRepoPWD(rec repo.Repo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "alias:   %s\n", rec.Alias)
	fmt.Fprintf(&b, "url:     %s\n", rec.URL)
	fmt.Fprintf(&b, "path:    %s\n", rec.Path)
	if rec.DefaultBranch != "" {
		fmt.Fprintf(&b, "default: %s\n", rec.DefaultBranch)
	}
	fmt.Fprintf(&b, "current: %s\n", firstNonEmpty(rec.CurrentBranch, "?"))
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}


