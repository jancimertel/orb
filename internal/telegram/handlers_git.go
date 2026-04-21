package telegram

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/repo"
	"github.com/jancimertel/orb/internal/state"
)

// Callback data vocabulary: "git:<action>:<opID>".
const (
	gitPrefix         = "git:"
	gitActionOK       = "ok"
	gitActionCancel   = "cancel"
	gitActionCheckout = "co"
)

// registerGit wires /branch, /diff, /commit, /push, /pull, /status-git plus
// the git:* callback handler. Replaces the stubs that previously sat here.
func (r *Router) registerGit(h *th.BotHandler) {
	h.Handle(r.handleBranch, th.CommandEqual("branch"))
	h.Handle(r.handleCommit, th.CommandEqual("commit"))
	h.Handle(r.handlePush, th.CommandEqual("push"))
	h.Handle(r.handlePull, th.CommandEqual("pull"))
	h.Handle(r.handleDiff, th.CommandEqual("diff"))
	h.Handle(r.handleStatusGit, th.CommandEqual("status-git"))
	h.Handle(r.handleGitCallback, th.CallbackDataPrefix(gitPrefix))
}

// --- read-only: /diff, /status-git ----------------------------------------

func (r *Router) handleDiff(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	rec, ok := r.requireActiveRepo(ctx, chatID)
	if !ok {
		return nil
	}
	staged := strings.Contains(argAfterCommand(u.Message.Text), "--staged")
	out, err := repo.Diff(ctx, rec.Path, staged)
	if err != nil && out == "" {
		r.logger.Error("git diff failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "diff failed: "+truncate(err.Error(), 400))
	}
	if strings.TrimSpace(out) == "" {
		return r.reply(ctx, chatID, "no diff")
	}
	return r.replyChunked(ctx, chatID, out)
}

func (r *Router) handleStatusGit(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	rec, ok := r.requireActiveRepo(ctx, chatID)
	if !ok {
		return nil
	}
	out, err := repo.Status(ctx, rec.Path)
	if err != nil {
		return r.reply(ctx, chatID, "status failed: "+truncate(err.Error(), 400))
	}
	if strings.TrimSpace(out) == "" {
		return r.reply(ctx, chatID, "clean working tree")
	}
	return r.replyChunked(ctx, chatID, out)
}

// --- write ops: previews + confirm keyboard -------------------------------

func (r *Router) handleBranch(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	name := strings.TrimSpace(argAfterCommand(u.Message.Text))
	rec, ok := r.requireActiveRepo(ctx, chatID)
	if !ok {
		return nil
	}

	if name == "" {
		return r.sendBranchPicker(ctx, chatID, rec)
	}

	if err := repo.ValidateBranchName(name); err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	cur, _ := repo.CurrentBranch(ctx, rec.Path)

	preview := fmt.Sprintf("Create + checkout branch?\n  repo:   %s\n  from:   %s\n  new:    %s",
		rec.Alias, firstNonEmpty(cur, "?"), name)
	return r.sendGitConfirm(ctx, chatID, preview, &pendingGitOp{
		chatID: chatID,
		kind:   gitOpBranch,
		alias:  rec.Alias,
		branch: name,
	})
}

// sendBranchPicker lists local branches as inline buttons. Tapping a button
// triggers an immediate `git checkout <branch>` (no separate confirm step —
// checkout is reversible and git refuses the switch when the tree is dirty).
func (r *Router) sendBranchPicker(ctx *th.Context, chatID int64, rec state.Repo) error {
	branches, err := repo.LocalBranches(ctx, rec.Path)
	if err != nil {
		return r.reply(ctx, chatID, "list branches failed: "+truncate(err.Error(), 400))
	}
	if len(branches) == 0 {
		return r.reply(ctx, chatID, "no local branches in "+rec.Alias)
	}
	cur, _ := repo.CurrentBranch(ctx, rec.Path)

	rows := make([][]telego.InlineKeyboardButton, 0, len(branches))
	for _, b := range branches {
		label := b
		if b == cur {
			label = "✓ " + b
		}
		op := &pendingGitOp{
			chatID: chatID,
			kind:   gitOpCheckout,
			alias:  rec.Alias,
			branch: b,
		}
		opID := r.gitConfirms.Register(op)
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label, CallbackData: gitPrefix + gitActionCheckout + ":" + opID},
		})
	}

	hint := fmt.Sprintf("Branches in %s — tap to checkout (current marked ✓):", rec.Alias)
	_, err = r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        hint,
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) handleCommit(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	msg := strings.TrimSpace(argAfterCommand(u.Message.Text))
	if msg == "" {
		return r.reply(ctx, chatID, "usage: /commit <message>")
	}
	rec, ok := r.requireActiveRepo(ctx, chatID)
	if !ok {
		return nil
	}
	status, _ := repo.StatusShort(ctx, rec.Path)
	if strings.TrimSpace(status) == "" {
		return r.reply(ctx, chatID, "nothing to commit — working tree clean")
	}

	preview := fmt.Sprintf("Commit in %s?\n\nmessage:\n%s\n\nfiles (git add -A):\n%s",
		rec.Alias, truncate(msg, 400), truncate(status, 1200))
	return r.sendGitConfirm(ctx, chatID, preview, &pendingGitOp{
		chatID:    chatID,
		kind:      gitOpCommit,
		alias:     rec.Alias,
		commitMsg: msg,
	})
}

func (r *Router) handlePush(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))
	if strings.Contains(arg, "--force") || strings.Contains(arg, "-f") {
		return r.reply(ctx, chatID, "force pushes are disabled")
	}
	rec, ok := r.requireActiveRepo(ctx, chatID)
	if !ok {
		return nil
	}
	branch, _ := repo.CurrentBranch(ctx, rec.Path)
	if branch == "" || branch == "HEAD" {
		return r.reply(ctx, chatID, "detached HEAD — nothing to push")
	}

	preview := fmt.Sprintf("Push?\n  repo:   %s\n  branch: %s\n  remote: %s",
		rec.Alias, branch, rec.URL)
	return r.sendGitConfirm(ctx, chatID, preview, &pendingGitOp{
		chatID: chatID,
		kind:   gitOpPush,
		alias:  rec.Alias,
		branch: branch,
	})
}

func (r *Router) handlePull(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	rec, ok := r.requireActiveRepo(ctx, chatID)
	if !ok {
		return nil
	}
	branch, _ := repo.CurrentBranch(ctx, rec.Path)
	if branch == "" || branch == "HEAD" {
		return r.reply(ctx, chatID, "detached HEAD — nothing to pull")
	}

	preview := fmt.Sprintf("Pull (--ff-only)?\n  repo:   %s\n  branch: %s\n  remote: %s",
		rec.Alias, branch, rec.URL)
	return r.sendGitConfirm(ctx, chatID, preview, &pendingGitOp{
		chatID: chatID,
		kind:   gitOpPull,
		alias:  rec.Alias,
		branch: branch,
	})
}

// sendGitConfirm registers op, sends the preview with a [Confirm] [Cancel]
// keyboard, and records the message ID on the pending entry.
func (r *Router) sendGitConfirm(ctx *th.Context, chatID int64, preview string, op *pendingGitOp) error {
	opID := r.gitConfirms.Register(op)
	kb := &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{
				{Text: "✅ Confirm", CallbackData: gitPrefix + gitActionOK + ":" + opID},
				{Text: "❌ Cancel", CallbackData: gitPrefix + gitActionCancel + ":" + opID},
			},
		},
	}
	msg, err := r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        preview,
		ReplyMarkup: kb,
	})
	if err != nil {
		r.gitConfirms.Take(opID)
		return err
	}
	op.SetMessage(msg.MessageID)
	return nil
}

// --- callback dispatch ----------------------------------------------------

func (r *Router) handleGitCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	action, opID, ok := parseGitCallback(cq.Data)
	if !ok {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	op, taken := r.gitConfirms.Take(opID)
	if !taken {
		return r.answerCallback(ctx, cq.ID, "expired")
	}

	switch action {
	case gitActionCancel:
		_ = r.answerCallback(ctx, cq.ID, "cancelled")
		r.editCallbackMessage(ctx, cq, "❌ cancelled")
		return nil
	case gitActionOK, gitActionCheckout:
		_ = r.answerCallback(ctx, cq.ID, "executing…")
		return r.executeGitOp(ctx, cq, op)
	}
	return r.answerCallback(ctx, cq.ID, "unknown action")
}

// executeGitOp runs the confirmed op and edits the prompt message with the
// outcome (or sends a follow-up for large output).
func (r *Router) executeGitOp(ctx *th.Context, cq *telego.CallbackQuery, op *pendingGitOp) error {
	glog := r.logger.With(
		"chat_id", op.chatID,
		"kind", string(op.kind),
		"alias", op.alias,
	)
	started := time.Now()

	rec, err := r.store.GetRepo(ctx, op.chatID, op.alias)
	if err != nil {
		glog.Warn("git op: repo lookup failed", "err", err)
		r.editCallbackMessage(ctx, cq, gitErrorBody(op, "repo lookup failed: "+err.Error()))
		return nil
	}

	var (
		header string
		output string
		opErr  error
	)
	switch op.kind {
	case gitOpBranch:
		header = fmt.Sprintf("✅ branch %s created in %s", op.branch, op.alias)
		output, opErr = repo.CreateBranch(ctx, rec.Path, op.branch)
	case gitOpCommit:
		header = fmt.Sprintf("✅ committed in %s", op.alias)
		output, opErr = repo.CommitAll(ctx, rec.Path, op.commitMsg)
	case gitOpPush:
		header = fmt.Sprintf("✅ pushed %s from %s", op.branch, op.alias)
		output, opErr = repo.Push(ctx, rec.Path, rec.URL, r.cfg.GithubPAT, op.branch)
	case gitOpPull:
		header = fmt.Sprintf("✅ pulled %s into %s", op.branch, op.alias)
		output, opErr = repo.Pull(ctx, rec.Path, rec.URL, r.cfg.GithubPAT, op.branch)
	case gitOpCheckout:
		header = fmt.Sprintf("✅ checked out %s in %s", op.branch, op.alias)
		output, opErr = repo.Checkout(ctx, rec.Path, op.branch)
	default:
		r.editCallbackMessage(ctx, cq, "unknown op kind")
		return nil
	}

	if opErr != nil {
		glog.Warn("git op failed",
			"err", opErr,
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
		r.status.RecordError(op.chatID, "git "+string(op.kind), truncate(opErr.Error(), 400))
		body := gitErrorBody(op, opErr.Error())
		if output != "" {
			body += "\n\n" + truncate(output, 1200)
		}
		r.editCallbackMessage(ctx, cq, body)
		return nil
	}
	glog.Info("git op succeeded",
		"branch", op.branch,
		"elapsed_ms", time.Since(started).Milliseconds(),
	)

	body := header
	if strings.TrimSpace(output) != "" {
		body += "\n\n" + truncate(output, 1500)
	}
	r.editCallbackMessage(ctx, cq, body)
	return nil
}

// --- helpers --------------------------------------------------------------

func parseGitCallback(data string) (action, opID string, ok bool) {
	if !strings.HasPrefix(data, gitPrefix) {
		return "", "", false
	}
	rest := data[len(gitPrefix):]
	i := strings.IndexByte(rest, ':')
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

func gitErrorBody(op *pendingGitOp, reason string) string {
	return fmt.Sprintf("❌ %s failed in %s\n\n%s", op.kind, op.alias, reason)
}

// editCallbackMessage replaces the prompt message (keyboard and all) with
// text. Best-effort — logs but doesn't fail the handler when the edit errors.
func (r *Router) editCallbackMessage(ctx *th.Context, cq *telego.CallbackQuery, text string) {
	if cq.Message == nil {
		return
	}
	_, err := r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:    telego.ChatID{ID: cq.Message.GetChat().ID},
		MessageID: cq.Message.GetMessageID(),
		Text:      truncate(text, telegramMsgLimit),
	})
	if err != nil {
		r.logger.Debug("edit callback message failed", "err", err)
	}
}

// requireActiveRepo fetches the active repo or replies with guidance when
// none is selected. Returns (rec, true) only on success.
func (r *Router) requireActiveRepo(ctx *th.Context, chatID int64) (state.Repo, bool) {
	cs, err := r.chatStateOrEmpty(ctx, chatID)
	if err != nil {
		r.logger.Error("chat state failed", "chat_id", chatID, "err", err)
		_ = r.reply(ctx, chatID, "internal error — check logs")
		return state.Repo{}, false
	}
	if cs.ActiveRepoAlias == "" {
		_ = r.reply(ctx, chatID, "no active repo — /repo select <alias>")
		return state.Repo{}, false
	}
	rec, err := r.store.GetRepo(ctx, chatID, cs.ActiveRepoAlias)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			_ = r.reply(ctx, chatID, fmt.Sprintf("active alias %q missing — /repo list", cs.ActiveRepoAlias))
		} else {
			_ = r.reply(ctx, chatID, "repo lookup failed — check logs")
		}
		return state.Repo{}, false
	}
	return *rec, true
}

// replyChunked splits long output into multiple messages, preserving newline
// boundaries where possible.
func (r *Router) replyChunked(ctx *th.Context, chatID int64, body string) error {
	for _, chunk := range chunkMessage(body, telegramMsgLimit) {
		if err := r.reply(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}
