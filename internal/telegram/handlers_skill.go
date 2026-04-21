package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// registerSkill wires /skill — read-only listing of native Claude Code
// skills under `.claude/skills/<name>/SKILL.md`. These are auto-discovered
// by Claude inside the subprocess based on their description; the bot
// doesn't inject or invoke them. `/skill` exists for operator visibility:
// see what's installed, peek a skill's body.
//
// For explicit one-shot preambles, use `/command`.
func (r *Router) registerSkill(h *th.BotHandler) {
	h.Handle(r.handleSkill, th.CommandEqual("skill"))
}

func (r *Router) handleSkill(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))
	head, rest, _ := strings.Cut(arg, " ")

	switch head {
	case "":
		return r.showSkillList(ctx, chatID)
	case "reload":
		r.agents.Reload()
		return r.reply(ctx, chatID, "skills reloaded")
	case "info":
		name := strings.TrimSpace(rest)
		if name == "" {
			return r.reply(ctx, chatID, "usage: /skill info <name>")
		}
		return r.showSkillInfo(ctx, chatID, name)
	default:
		return r.reply(ctx, chatID,
			"usage: /skill | /skill info <name> | /skill reload\n\n"+
				"Skills are auto-discovered by Claude; you can't invoke them. "+
				"For explicit one-shot preambles, use /command.")
	}
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
			"no skills installed.\n\nDrop <name>/SKILL.md into "+
				r.cfg.HomeDir+"/.claude/skills/ or <repo>/.claude/skills/\n"+
				"See examples/.claude/skills/ in the bot repo for the schema.\n\n"+
				"Skills are auto-selected by Claude based on their description; "+
				"this command lists what's available, but doesn't invoke them.")
	}

	var sb strings.Builder
	sb.WriteString("Installed skills (auto-discovered by Claude — /skill info <name> for details):\n\n")
	for _, s := range skills {
		scope := ""
		if s.Scope == "repo" {
			scope = " (repo)"
		}
		fmt.Fprintf(&sb, "• %s%s\n", s.Name, scope)
		if s.Description != "" {
			fmt.Fprintf(&sb, "    %s\n", truncate(s.Description, 160))
		}
	}
	sb.WriteString("\nFor explicit one-shot preambles, see /command.")
	return r.reply(ctx, chatID, sb.String())
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
	fmt.Fprintf(&sb, "Source: %s\n\n--- body ---\n%s", s.Source, s.Body)
	for _, chunk := range chunkMessage(sb.String(), 3900) {
		if err := r.reply(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}
