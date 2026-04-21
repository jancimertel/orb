package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/agent"
)

// registerAgent wires /agent and its callback prefix. The command toggles the
// chat-sticky system prompt that gets appended to Claude via
// --append-system-prompt on every spawn. Subcommands: <name> | info <name> |
// off | reload; no-arg lists agents as inline buttons.
func (r *Router) registerAgent(h *th.BotHandler) {
	h.Handle(r.handleAgent, th.CommandEqual("agent"))
	h.Handle(r.handleAgentCallback, th.CallbackDataPrefix(agentPrefix))
}

const (
	agentPrefix    = "agent:"
	agentActionSet = "set"
)

func (r *Router) handleAgent(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))

	// No argument → list with inline buttons.
	if arg == "" {
		return r.showAgentList(ctx, chatID)
	}

	// Subcommands.
	head, rest, _ := strings.Cut(arg, " ")
	switch head {
	case "off":
		return r.setAgent(ctx, chatID, "")
	case "reload":
		r.agents.Reload()
		return r.reply(ctx, chatID, "agents reloaded")
	case "info":
		name := strings.TrimSpace(rest)
		if name == "" {
			return r.reply(ctx, chatID, "usage: /agent info <name>")
		}
		return r.showAgentInfo(ctx, chatID, name)
	default:
		return r.setAgent(ctx, chatID, arg)
	}
}

func (r *Router) showAgentList(ctx *th.Context, chatID int64) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	agents, err := r.agents.List(repoRoot)
	if err != nil {
		r.logger.Error("agents list failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "agent lookup failed — check logs")
	}
	active := r.activeAgentName(ctx, chatID)

	if len(agents) == 0 {
		return r.reply(ctx, chatID,
			"no agents installed.\n\nDrop .md files into "+
				r.cfg.HomeDir+"/.claude/agents/ or <repo>/.claude/agents/\n"+
				"Each file needs a YAML frontmatter with `name:` and `description:`.")
	}

	var sb strings.Builder
	sb.WriteString("Active agent: ")
	if active == "" {
		sb.WriteString("(none)")
	} else {
		sb.WriteString(active)
	}
	sb.WriteString("\n\nSelect:")

	var rows [][]telego.InlineKeyboardButton
	for _, a := range agents {
		label := a.Name
		if a.Scope == "repo" {
			label += " (repo)"
		}
		if a.Name == active {
			label = "✓ " + label
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label, CallbackData: agentPrefix + agentActionSet + ":" + a.Name},
		})
	}
	rows = append(rows, []telego.InlineKeyboardButton{
		{Text: "(clear)", CallbackData: agentPrefix + agentActionSet + ":"},
	})

	_, err = r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        sb.String(),
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) showAgentInfo(ctx *th.Context, chatID int64, name string) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	a, err := r.agents.Get(repoRoot, name)
	if err != nil {
		return r.reply(ctx, chatID, fmt.Sprintf("agent %q not found", name))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Agent: %s (%s)\n", a.Name, a.Scope)
	if a.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", a.Description)
	}
	fmt.Fprintf(&sb, "Source: %s\n\n--- body ---\n%s", a.Source, a.Body)
	return r.reply(ctx, chatID, sb.String())
}

func (r *Router) handleAgentCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	rest := strings.TrimPrefix(cq.Data, agentPrefix)
	action, name, ok := strings.Cut(rest, ":")
	if !ok || action != agentActionSet {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	if err := r.applyAgentChange(ctx, chatID, name); err != nil {
		return r.answerCallback(ctx, cq.ID, err.Error())
	}
	label := "set"
	if name == "" {
		label = "cleared"
	}
	_ = r.answerCallback(ctx, cq.ID, label)

	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      agentChangeMessage(name),
		})
	}
	return nil
}

// setAgent resolves name against the loader (empty = clear), persists, and
// resets the runner so the next spawn picks up the new --append-system-prompt.
func (r *Router) setAgent(ctx *th.Context, chatID int64, name string) error {
	if err := r.applyAgentChange(ctx, chatID, name); err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	return r.reply(ctx, chatID, agentChangeMessage(name))
}

// agentChangeMessage is the confirmation shown after /agent set or clear.
// Surfaces the session-reset behavior so the user knows context was cleared.
func agentChangeMessage(name string) string {
	if name == "" {
		return "agent cleared.\nSession reset — next turn starts fresh."
	}
	return "agent set to " + name + ".\nSession reset — next turn starts fresh with the new voice. Use /session to resume an older one."
}

func (r *Router) applyAgentChange(ctx *th.Context, chatID int64, name string) error {
	if name != "" {
		repoRoot := r.activeRepoRoot(ctx, chatID)
		if _, err := r.agents.Get(repoRoot, name); err != nil {
			return fmt.Errorf("agent %q not found", name)
		}
	}
	if err := r.store.SetActiveAgent(ctx, chatID, name); err != nil {
		r.logger.Error("set agent failed", "chat_id", chatID, "err", err)
		return fmt.Errorf("persist failed")
	}
	// Also clear the active session. --append-system-prompt on --resume is
	// not reliably honored (resumed sessions reuse their stored system
	// prompt), so forcing a fresh session guarantees the new agent voice
	// takes effect on the next turn. The old JSONL is still on disk; the
	// operator can resume it via /session if they want that history back.
	if err := r.store.SetActiveSession(ctx, chatID, ""); err != nil {
		r.logger.Warn("clear session on agent change failed", "chat_id", chatID, "err", err)
	}
	r.registry.Reset(chatID)
	r.sessionAllow.Clear(chatID)
	return nil
}

// --- helpers shared with driveTurn ---------------------------------------

// activeRepoRoot returns the filesystem path of the active repo for chatID,
// or "" if none is selected. Used by the loader to pick up repo-local agent
// overrides.
func (r *Router) activeRepoRoot(ctx *th.Context, chatID int64) string {
	cs, _ := r.chatStateOrEmpty(ctx, chatID)
	if cs.ActiveRepoAlias == "" {
		return ""
	}
	rec, err := r.store.GetRepo(ctx, chatID, cs.ActiveRepoAlias)
	if err != nil {
		return ""
	}
	return rec.Path
}

// activeAgentName returns the persisted agent name, or auto-resolves to
// "default" if the active repo ships .claude/agents/default.md and the chat
// has not explicitly set an agent. Empty means no system-prompt append.
func (r *Router) activeAgentName(ctx *th.Context, chatID int64) string {
	cs, _ := r.chatStateOrEmpty(ctx, chatID)
	if cs.ActiveAgent != "" {
		return cs.ActiveAgent
	}
	// Repo-scoped default: if the selected repo has agents/default.md, use it.
	repoRoot := r.activeRepoRoot(ctx, chatID)
	if repoRoot == "" {
		return ""
	}
	if def, err := r.agents.Get(repoRoot, "default"); err == nil && def.Scope == "repo" {
		return "default"
	}
	return ""
}

// resolveAgentBody returns the body of the active agent, or empty if none.
func (r *Router) resolveAgentBody(ctx *th.Context, chatID int64) string {
	name := r.activeAgentName(ctx, chatID)
	if name == "" {
		return ""
	}
	repoRoot := r.activeRepoRoot(ctx, chatID)
	a, err := r.agents.Get(repoRoot, name)
	if err != nil {
		r.logger.Warn("active agent missing", "chat_id", chatID, "name", name, "err", err)
		return ""
	}
	return a.Body
}

// compile-time guard that the agent loader is wired.
var _ = (*agent.Loader)(nil)
