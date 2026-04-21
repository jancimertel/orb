package telegram

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/agent"
)

// registerSkill wires /skill and its callback prefix. A skill is a one-shot
// preamble loaded from .claude/commands/<name>.md. It does NOT persist
// across turns — that's what agents are for.
func (r *Router) registerSkill(h *th.BotHandler) {
	h.Handle(r.handleSkill, th.CommandEqual("skill"))
	h.Handle(r.handleSkillCallback, th.CallbackDataPrefix(skillPrefix))
}

const (
	skillPrefix    = "skill:"
	skillActionArm = "arm"
)

// pendingSkills tracks the "armed" skill per chat — set by /skill <name>
// with no text, consumed by the next non-command text message.
type pendingSkillStore struct {
	mu sync.Mutex
	m  map[int64]string
}

func newPendingSkillStore() *pendingSkillStore {
	return &pendingSkillStore{m: map[int64]string{}}
}

func (p *pendingSkillStore) Arm(chatID int64, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[chatID] = name
}

// Take returns the armed skill (if any) and clears the slot. Single-shot.
func (p *pendingSkillStore) Take(chatID int64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	name := p.m[chatID]
	delete(p.m, chatID)
	return name
}

func (p *pendingSkillStore) Peek(chatID int64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[chatID]
}

func (p *pendingSkillStore) Clear(chatID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, chatID)
}

func (r *Router) handleSkill(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))

	if arg == "" {
		return r.showSkillList(ctx, chatID)
	}

	head, rest, _ := strings.Cut(arg, " ")
	switch head {
	case "reload":
		r.agents.Reload()
		return r.reply(ctx, chatID, "skills reloaded")
	case "info":
		name := strings.TrimSpace(rest)
		if name == "" {
			return r.reply(ctx, chatID, "usage: /skill info <name>")
		}
		return r.showSkillInfo(ctx, chatID, name)
	}

	// Default: `/skill <name>` (arm) or `/skill <name> <text>` (one-shot).
	name := head
	userText := strings.TrimSpace(rest)

	sk, err := r.lookupSkill(ctx, chatID, name)
	if err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	if userText == "" {
		r.pendingSkills.Arm(chatID, sk.Name)
		return r.reply(ctx, chatID,
			fmt.Sprintf("🎯 Skill %q armed — send your next message and it will be applied.", sk.Name))
	}
	return r.runSkill(ctx, chatID, sk, userText)
}

func (r *Router) showSkillList(ctx *th.Context, chatID int64) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	skills, err := r.agents.Skills(repoRoot)
	if err != nil {
		r.logger.Error("skills list failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "skill lookup failed — check logs")
	}
	if len(skills) == 0 {
		return r.reply(ctx, chatID,
			"no skills installed.\n\nDrop .md files into "+
				r.cfg.HomeDir+"/.claude/commands/ or <repo>/.claude/commands/\n"+
				"See examples/.claude/commands/ in the bot repo for the schema.")
	}

	var rows [][]telego.InlineKeyboardButton
	for _, s := range skills {
		label := s.Name
		if s.Scope == "repo" {
			label += " (repo)"
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label, CallbackData: skillPrefix + skillActionArm + ":" + s.Name},
		})
	}
	_, err = r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        "Select a skill to arm for your next message:",
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) showSkillInfo(ctx *th.Context, chatID int64, name string) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	s, err := r.agents.Skill(repoRoot, name)
	if err != nil {
		return r.reply(ctx, chatID, fmt.Sprintf("skill %q not found", name))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Skill: %s (%s)\n", s.Name, s.Scope)
	if s.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", s.Description)
	}
	if s.RequiresRepo {
		sb.WriteString("Requires repo: yes\n")
	}
	if len(s.Triggers) > 0 {
		fmt.Fprintf(&sb, "Triggers: %s\n", strings.Join(s.Triggers, ", "))
	}
	fmt.Fprintf(&sb, "Source: %s\n\n--- body ---\n%s", s.Source, s.Body)
	return r.reply(ctx, chatID, sb.String())
}

func (r *Router) handleSkillCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	rest := strings.TrimPrefix(cq.Data, skillPrefix)
	action, name, ok := strings.Cut(rest, ":")
	if !ok || action != skillActionArm || name == "" {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	if _, err := r.lookupSkill(ctx, chatID, name); err != nil {
		return r.answerCallback(ctx, cq.ID, "not found")
	}
	r.pendingSkills.Arm(chatID, name)
	_ = r.answerCallback(ctx, cq.ID, "armed")
	if cq.Message != nil {
		_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: chatID},
			MessageID: cq.Message.GetMessageID(),
			Text:      fmt.Sprintf("🎯 Skill %q armed — send your next message.", name),
		})
	}
	return nil
}

// lookupSkill resolves a skill against the chat's active repo; also enforces
// x-requires-repo if the skill demands one and none is selected.
func (r *Router) lookupSkill(ctx *th.Context, chatID int64, name string) (agent.Skill, error) {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	s, err := r.agents.Skill(repoRoot, name)
	if err != nil {
		return agent.Skill{}, fmt.Errorf("skill %q not found", name)
	}
	if s.RequiresRepo && repoRoot == "" {
		return agent.Skill{}, fmt.Errorf("skill %q requires an active repo — use /cd <alias> first", name)
	}
	return s, nil
}

// runSkill composes the final prompt and drives the turn.
//
// If the body contains $ARGUMENTS, we substitute `userText` and send only the
// rendered body (matches native Claude Code command semantics). Otherwise we
// send "<body>\n\n<userText>" so the user's input doesn't get swallowed.
//
// A receipt message is sent BEFORE the turn so the operator knows which
// skill was applied. Phase D will move this into the streaming renderer as
// a dimmed top-of-reply status line.
func (r *Router) runSkill(ctx *th.Context, chatID int64, s agent.Skill, userText string) error {
	if err := r.reply(ctx, chatID, "🎯 Skill: "+s.Name); err != nil {
		// reply already logs; keep going — a missed receipt is not fatal.
	}

	var prompt string
	if strings.Contains(s.Body, "$ARGUMENTS") {
		prompt = s.RenderBody(userText)
	} else {
		prompt = s.Body + "\n\n" + userText
	}
	return r.driveTurn(ctx, chatID, prompt)
}
