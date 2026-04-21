package telegram

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/agent"
)

// registerCommand wires /command and its callback prefix. A command is a
// one-shot preamble loaded from .claude/commands/<name>.md (native Claude
// Code slash-command format). It does NOT persist across turns — that's
// what agents are for. Distinct from /skill, which lists native
// auto-discovered skills in .claude/skills/<name>/SKILL.md.
func (r *Router) registerCommand(h *th.BotHandler) {
	h.Handle(r.handleCommand, th.CommandEqual("command"))
	h.Handle(r.handleCommandCallback, th.CallbackDataPrefix(commandPrefix))
}

const (
	commandPrefix    = "cmd:"
	commandActionArm = "arm"
)

// pendingCommandStore tracks the "armed" command per chat — set by
// `/command <name>` with no text, consumed by the next non-command text
// message.
type pendingCommandStore struct {
	mu sync.Mutex
	m  map[int64]string
}

func newPendingCommandStore() *pendingCommandStore {
	return &pendingCommandStore{m: map[int64]string{}}
}

func (p *pendingCommandStore) Arm(chatID int64, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[chatID] = name
}

// Take returns the armed command (if any) and clears the slot. Single-shot.
func (p *pendingCommandStore) Take(chatID int64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	name := p.m[chatID]
	delete(p.m, chatID)
	return name
}

func (p *pendingCommandStore) Peek(chatID int64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[chatID]
}

func (p *pendingCommandStore) Clear(chatID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, chatID)
}

func (r *Router) handleCommand(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))

	if arg == "" {
		return r.showCommandList(ctx, chatID)
	}

	head, rest, _ := strings.Cut(arg, " ")
	switch head {
	case "reload":
		r.agents.Reload()
		return r.reply(ctx, chatID, "commands reloaded")
	case "info":
		name := strings.TrimSpace(rest)
		if name == "" {
			return r.reply(ctx, chatID, "usage: /command info <name>")
		}
		return r.showCommandInfo(ctx, chatID, name)
	}

	// Default: `/command <name>` (arm) or `/command <name> <text>` (one-shot).
	name := head
	userText := strings.TrimSpace(rest)

	cm, err := r.lookupCommand(ctx, chatID, name)
	if err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	if userText == "" {
		r.pendingCommands.Arm(chatID, cm.Name)
		return r.reply(ctx, chatID,
			fmt.Sprintf("🎯 Command %q armed — send your next message and it will be applied.", cm.Name))
	}
	return r.runCommand(ctx, chatID, cm, userText)
}

func (r *Router) showCommandList(ctx *th.Context, chatID int64) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	cmds, err := r.agents.Commands(repoRoot)
	if err != nil {
		r.logger.Error("commands list failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "command lookup failed — check logs")
	}
	if len(cmds) == 0 {
		return r.reply(ctx, chatID,
			"no commands installed.\n\nDrop .md files into "+
				r.cfg.HomeDir+"/.claude/commands/ or <repo>/.claude/commands/\n"+
				"See examples/.claude/commands/ in the bot repo for the schema.")
	}

	var rows [][]telego.InlineKeyboardButton
	for _, c := range cmds {
		label := c.Name
		if c.Scope == "repo" {
			label += " (repo)"
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label, CallbackData: commandPrefix + commandActionArm + ":" + c.Name},
		})
	}
	_, err = r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        "Select a command to arm for your next message:",
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) showCommandInfo(ctx *th.Context, chatID int64, name string) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	c, err := r.agents.Command(repoRoot, name)
	if err != nil {
		return r.reply(ctx, chatID, fmt.Sprintf("command %q not found", name))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Command: %s (%s)\n", c.Name, c.Scope)
	if c.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", c.Description)
	}
	if c.RequiresRepo {
		sb.WriteString("Requires repo: yes\n")
	}
	if len(c.Triggers) > 0 {
		fmt.Fprintf(&sb, "Triggers: %s\n", strings.Join(c.Triggers, ", "))
	}
	fmt.Fprintf(&sb, "Source: %s\n\n--- body ---\n%s", c.Source, c.Body)
	return r.reply(ctx, chatID, sb.String())
}

func (r *Router) handleCommandCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	rest := strings.TrimPrefix(cq.Data, commandPrefix)
	action, name, ok := strings.Cut(rest, ":")
	if !ok || action != commandActionArm || name == "" {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	if _, err := r.lookupCommand(ctx, chatID, name); err != nil {
		return r.answerCallback(ctx, cq.ID, "not found")
	}
	r.pendingCommands.Arm(chatID, name)
	_ = r.answerCallback(ctx, cq.ID, "armed")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      fmt.Sprintf("🎯 Command %q armed — send your next message.", name),
		})
	}
	return nil
}

// lookupCommand resolves a command against the chat's active repo; also
// enforces x-requires-repo if the command demands one and none is selected.
func (r *Router) lookupCommand(ctx *th.Context, chatID int64, name string) (agent.Command, error) {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	c, err := r.agents.Command(repoRoot, name)
	if err != nil {
		return agent.Command{}, fmt.Errorf("command %q not found", name)
	}
	if c.RequiresRepo && repoRoot == "" {
		return agent.Command{}, fmt.Errorf("command %q requires an active repo — use /cd <alias> first", name)
	}
	return c, nil
}

// runCommand composes the final prompt and drives the turn.
//
// If the body contains $ARGUMENTS, we substitute `userText` and send only
// the rendered body (matches native Claude Code command semantics).
// Otherwise we send "<body>\n\n<userText>" so the user's input doesn't get
// swallowed.
//
// A receipt message is sent BEFORE the turn so the operator knows which
// command was applied.
func (r *Router) runCommand(ctx *th.Context, chatID int64, c agent.Command, userText string) error {
	if err := r.reply(ctx, chatID, "🎯 Command: "+c.Name); err != nil {
		// reply already logs; keep going — a missed receipt is not fatal.
	}

	var prompt string
	if strings.Contains(c.Body, "$ARGUMENTS") {
		prompt = c.RenderBody(userText)
	} else {
		prompt = c.Body + "\n\n" + userText
	}
	return r.driveTurn(ctx, chatID, prompt)
}
