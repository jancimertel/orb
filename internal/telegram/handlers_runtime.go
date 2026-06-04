package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/catalog"
	"github.com/jancimertel/orb/internal/state"
)

const helpText = `Claude Code — Telegram bridge

General
  /start            Greeting
  /help             This message

Sessions
  /new              Start a fresh Claude session for this chat
  /session [N]      List recent sessions (default 10); tap one to resume

Repositories
  /repo add <url>   Clone a repo (alias optional)
  /repo list        Show registered repos
  /repo select <a>  Set active repo
  /repo remove <a>  Remove a repo (confirm)
  /cd <alias>       Alias for /repo select
  /pwd              Show active repo + branch

Git (approval-gated)
  /branch <name>    Create & checkout branch (confirm)
  /diff [--staged]  Show diff (read-only, no confirm)
  /status-git       git status --short (read-only)
  /commit <msg>     Stage all + commit (confirm)
  /push             Push current branch (confirm, force disabled)
  /pull             Pull --ff-only into current branch (confirm)

Runtime
  /usage            Token + cost totals (today, MTD)
  /model [id]       List models or switch
  /effort [level]   Reasoning effort: low|medium|high|xhigh|max (blank=default)
  /agent [name]     List or switch chat-sticky agent (system prompt)
  /command [name]   One-shot preamble; /command <n> <text> runs now, /command <n> arms next msg
  /skill            List native skills auto-discovered by Claude (/skill info <n> for body)
  /plugins          List installed Claude Code plugins (provisioned at startup)
  /remember <text>  Append to memory (auto-scope: project if repo active, else user)
  /memory           Summary; /memory show [scope], /memory clear <scope>, /memory path
  /context          Show what gets injected into the next turn (agent body, paths)
  /status           Active repo, session, model; runner state
  /cancel           Kill the current turn`

// Hard-coded model catalog. Used as-is when MODELS_API_KEY is unset; otherwise
// it is the fallback the catalog provider serves when the live /v1/models call
// is unavailable. Keep in sync with Anthropic's current lineup.
var availableModels = []modelEntry{
	{id: "claude-opus-4-8", label: "Opus 4.8"},
	{id: "claude-opus-4-7", label: "Opus 4.7"},
	{id: "claude-sonnet-4-6", label: "Sonnet 4.6"},
	{id: "claude-haiku-4-5-20251001", label: "Haiku 4.5"},
}

type modelEntry struct {
	id    string
	label string
}

// FallbackModels exposes the curated list as catalog.Model values for use as the
// provider's offline fallback. availableModels stays the single source of truth.
func FallbackModels() []catalog.Model {
	out := make([]catalog.Model, len(availableModels))
	for i, m := range availableModels {
		out[i] = catalog.Model{ID: m.id, Label: m.label}
	}
	return out
}

const (
	modelPrefix    = "model:"
	modelActionSet = "set"
)

// Effort levels accepted by the CLI's --effort flag. xhigh falls back to high
// on older models; max is unconstrained. Empty (not listed) = model default.
var availableEfforts = []effortEntry{
	{id: "low", label: "Low"},
	{id: "medium", label: "Medium"},
	{id: "high", label: "High"},
	{id: "xhigh", label: "X-High"},
	{id: "max", label: "Max"},
}

type effortEntry struct {
	id    string
	label string
}

const (
	effortPrefix    = "effort:"
	effortActionSet = "set"
)

// registerGeneral wires /start and /help.
func (r *Router) registerGeneral(h *th.BotHandler) {
	h.Handle(r.handleStart, th.CommandEqual("start"))
	h.Handle(r.handleHelp, th.CommandEqual("help"))
}

// registerRuntime wires /usage, /model, /status, /cancel and the model:*
// callback.
func (r *Router) registerRuntime(h *th.BotHandler) {
	h.Handle(r.handleUsage, th.CommandEqual("usage"))
	h.Handle(r.handleModel, th.CommandEqual("model"))
	h.Handle(r.handleStatus, th.CommandEqual("status"))
	h.Handle(r.handleCancel, th.CommandEqual("cancel"))
	h.Handle(r.handleModelCallback, th.CallbackDataPrefix(modelPrefix))
	h.Handle(r.handleEffort, th.CommandEqual("effort"))
	h.Handle(r.handleEffortCallback, th.CallbackDataPrefix(effortPrefix))
}

func (r *Router) handleStart(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	return r.reply(ctx, u.Message.Chat.ID,
		"bot online. Send /help for the command reference.")
}

func (r *Router) handleHelp(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	return r.reply(ctx, u.Message.Chat.ID, helpText)
}

// handleCancel kills the current Claude turn (if any) and clears the runner
// slot. The next message will spawn a fresh runner, resuming the stored
// session if set.
func (r *Router) handleCancel(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	r.sessionAllow.Clear(chatID)
	if r.registry.Cancel(chatID) {
		return r.reply(ctx, chatID, "cancelled")
	}
	return r.reply(ctx, chatID, "nothing to cancel")
}

// --- /usage ---------------------------------------------------------------

func (r *Router) handleUsage(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID

	today, err := r.store.UsageToday(ctx, chatID)
	if err != nil {
		r.logger.Error("usage today failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "usage lookup failed — check logs")
	}
	mtd, err := r.store.UsageMonthToDate(ctx, chatID)
	if err != nil {
		r.logger.Error("usage mtd failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "usage lookup failed — check logs")
	}

	var sb strings.Builder
	sb.WriteString("Usage — today (UTC):\n")
	sb.WriteString(formatUsageRow(today))
	sb.WriteString("\nMonth to date:\n")
	sb.WriteString(formatUsageRow(mtd))

	// Active session details, if any.
	cs, _ := r.chatStateOrEmpty(ctx, chatID)
	if cs.ActiveSessionID != "" {
		sb.WriteString("\nActive session:\n  id:       ")
		sb.WriteString(cs.ActiveSessionID)
		sb.WriteByte('\n')
		if meta, err := r.sessions.Find(cs.ActiveSessionID); err == nil {
			fmt.Fprintf(&sb, "  messages: %d\n  size:     %s\n",
				meta.MessageCount, humanBytes(meta.SizeBytes))
		} else {
			sb.WriteString("  (file not found — /new to reset)\n")
		}
	} else {
		sb.WriteString("\nActive session: (none)\n")
	}

	return r.reply(ctx, chatID, sb.String())
}

func formatUsageRow(u state.UsageRow) string {
	return fmt.Sprintf(
		"  input:      %s\n  output:     %s\n  cache read: %s\n  cache write:%s\n  cost USD:   $%.4f\n",
		humanInt(u.InputTokens),
		humanInt(u.OutputTokens),
		humanInt(u.CacheReadTokens),
		humanInt(u.CacheWriteTokens),
		u.CostUSD,
	)
}

// --- /model ---------------------------------------------------------------

func (r *Router) handleModel(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))

	if arg != "" {
		return r.setModel(ctx, chatID, arg)
	}

	active := r.activeModelFor(ctx, chatID)

	var rows [][]telego.InlineKeyboardButton
	for _, m := range r.catalog.Models(ctx) {
		label := m.Label
		if m.ID == active {
			label = "✓ " + label
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label + "  (" + m.ID + ")", CallbackData: modelPrefix + modelActionSet + ":" + m.ID},
		})
	}

	_, err := r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        "Current model: " + active + "\n\nSelect:",
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) handleModelCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	if !strings.HasPrefix(cq.Data, modelPrefix) {
		return nil
	}
	rest := cq.Data[len(modelPrefix):]
	action, id, ok := strings.Cut(rest, ":")
	if !ok || action != modelActionSet {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	if err := r.applyModelChange(ctx, chatID, id); err != nil {
		return r.answerCallback(ctx, cq.ID, err.Error())
	}
	_ = r.answerCallback(ctx, cq.ID, "set")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      "Model set to " + id + "\n(takes effect on next turn)",
		})
	}
	return nil
}

func (r *Router) setModel(ctx *th.Context, chatID int64, id string) error {
	if err := r.applyModelChange(ctx, chatID, id); err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	msg := "model set to " + id + "\n(takes effect on next turn)"
	if !r.knownModel(ctx, id) {
		msg = "⚠️ " + id + " isn't in the known list — trying it anyway; " +
			"it will fail on the next turn if the model doesn't exist.\n\n" + msg
	}
	return r.reply(ctx, chatID, msg)
}

// applyModelChange persists the model and tears down the runner so the next
// spawn picks up the new --model. It does NOT validate membership; callers
// decide messaging (button ids always come from the catalog; typed ids may be
// brand-new and are accepted with a warning).
func (r *Router) applyModelChange(ctx *th.Context, chatID int64, id string) error {
	if err := r.store.SetActiveModel(ctx, chatID, id); err != nil {
		r.logger.Error("set model failed", "chat_id", chatID, "err", err)
		return fmt.Errorf("persist failed")
	}
	r.registry.Reset(chatID)
	r.sessionAllow.Clear(chatID)
	return nil
}

func (r *Router) knownModel(ctx context.Context, id string) bool {
	return catalog.Contains(r.catalog.Models(ctx), id)
}

func (r *Router) activeModelFor(ctx *th.Context, chatID int64) string {
	cs, err := r.chatStateOrEmpty(ctx, chatID)
	if err == nil && cs.ActiveModel != "" {
		return cs.ActiveModel
	}
	return r.cfg.DefaultModel
}

// --- /effort --------------------------------------------------------------

func (r *Router) handleEffort(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))

	if arg != "" {
		return r.setEffort(ctx, chatID, arg)
	}

	active := r.activeEffortFor(ctx, chatID)

	var rows [][]telego.InlineKeyboardButton
	for _, e := range availableEfforts {
		label := e.label
		if e.id == active {
			label = "✓ " + label
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label, CallbackData: effortPrefix + effortActionSet + ":" + e.id},
		})
	}

	_, err := r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        "Current effort: " + effortDisplay(active) + "\n\nSelect:",
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) handleEffortCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	if !strings.HasPrefix(cq.Data, effortPrefix) {
		return nil
	}
	rest := cq.Data[len(effortPrefix):]
	action, id, ok := strings.Cut(rest, ":")
	if !ok || action != effortActionSet {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	if err := r.applyEffortChange(ctx, chatID, id); err != nil {
		return r.answerCallback(ctx, cq.ID, err.Error())
	}
	_ = r.answerCallback(ctx, cq.ID, "set")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      "Effort set to " + id + "\n(takes effect on next turn)",
		})
	}
	return nil
}

func (r *Router) setEffort(ctx *th.Context, chatID int64, id string) error {
	if err := r.applyEffortChange(ctx, chatID, id); err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	return r.reply(ctx, chatID, "effort set to "+id+"\n(takes effect on next turn)")
}

// applyEffortChange validates id against the known levels, persists it, and
// tears down the runner so the next spawn picks up the new --effort.
func (r *Router) applyEffortChange(ctx *th.Context, chatID int64, id string) error {
	if !knownEffort(id) {
		return fmt.Errorf("unknown effort %q", id)
	}
	if err := r.store.SetActiveEffort(ctx, chatID, id); err != nil {
		r.logger.Error("set effort failed", "chat_id", chatID, "err", err)
		return fmt.Errorf("persist failed")
	}
	r.registry.Reset(chatID)
	r.sessionAllow.Clear(chatID)
	return nil
}

func knownEffort(id string) bool {
	for _, e := range availableEfforts {
		if e.id == id {
			return true
		}
	}
	return false
}

func (r *Router) activeEffortFor(ctx *th.Context, chatID int64) string {
	cs, err := r.chatStateOrEmpty(ctx, chatID)
	if err == nil {
		return cs.ActiveEffort
	}
	return ""
}

// effortDisplay renders an empty effort as the model-default sentinel.
func effortDisplay(effort string) string {
	if effort == "" {
		return "model default"
	}
	return effort
}

// --- /status --------------------------------------------------------------

func (r *Router) handleStatus(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	cs, _ := r.chatStateOrEmpty(ctx, chatID)

	var sb strings.Builder
	fmt.Fprintf(&sb, "uptime:   %s\n", humanDuration(r.status.Uptime()))

	if cs.ActiveRepoAlias != "" {
		if rec, err := r.store.GetRepo(ctx, chatID, cs.ActiveRepoAlias); err == nil {
			fmt.Fprintf(&sb, "repo:     %s (%s)\n", rec.Alias, rec.Path)
		} else if errors.Is(err, state.ErrNotFound) {
			fmt.Fprintf(&sb, "repo:     %s (missing!)\n", cs.ActiveRepoAlias)
		}
	} else {
		sb.WriteString("repo:     (none)\n")
	}

	if cs.ActiveSessionID != "" {
		fmt.Fprintf(&sb, "session:  %s\n", cs.ActiveSessionID)
		if meta, err := r.sessions.Find(cs.ActiveSessionID); err == nil {
			fmt.Fprintf(&sb, "          %d msg, %s\n", meta.MessageCount, humanBytes(meta.SizeBytes))
		}
	} else {
		sb.WriteString("session:  (none — /new or /session)\n")
	}

	fmt.Fprintf(&sb, "model:    %s\n", r.activeModelFor(ctx, chatID))
	fmt.Fprintf(&sb, "effort:   %s\n", effortDisplay(r.activeEffortFor(ctx, chatID)))
	fmt.Fprintf(&sb, "runner:   %s\n", runnerState(r, chatID))

	if e, ok := r.status.LastError(chatID); ok {
		fmt.Fprintf(&sb, "last err: [%s, %s ago] %s\n",
			e.source, humanDuration(time.Since(e.at)), truncate(e.message, 400))
	}

	return r.reply(ctx, chatID, sb.String())
}

func runnerState(r *Router, chatID int64) string {
	switch {
	case r.registry.IsBusy(chatID):
		return "busy"
	case r.registry.HasRunner(chatID):
		return "idle"
	default:
		return "not started"
	}
}

// --- formatting helpers ---------------------------------------------------

// humanInt groups thousands with underscores for readability at a glance.
func humanInt(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	s := fmt.Sprintf("%d", n)
	// Insert "_" every 3 digits from the right.
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '_')
		}
		out = append(out, c)
	}
	return string(out)
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
