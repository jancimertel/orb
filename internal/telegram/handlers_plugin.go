package telegram

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/claude"
)

// registerPlugin wires /plugins — read-only listing of the Claude Code plugins
// provisioned onto $HOME/.claude at startup (see internal/claude EnsurePlugins).
// Plugins contribute skills/commands/agents the subprocess auto-loads; the bot
// doesn't invoke them directly. /plugins exists for operator visibility.
func (r *Router) registerPlugin(h *th.BotHandler) {
	h.Handle(r.handlePlugins, th.CommandEqual("plugins"))
}

func (r *Router) handlePlugins(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID

	plugins, err := claude.ListPlugins(r.cfg.HomeDir)
	if err != nil {
		r.logger.Error("plugin list failed", "chat_id", chatID, "err", err)
		return r.reply(ctx, chatID, "plugin lookup failed — check logs")
	}
	if len(plugins) == 0 {
		return r.reply(ctx, chatID,
			"no plugins installed.\n\nSet PLUGINS (comma-separated "+
				"<plugin>@<marketplace> keys) and restart the bot.")
	}

	var sb strings.Builder
	sb.WriteString("Loaded plugins (provisioned at startup; contribute skills/commands/agents):\n\n")
	for _, p := range plugins {
		status := "✓ enabled"
		if !p.Enabled {
			status = "✗ disabled"
		}
		ver := p.Version
		if ver == "" {
			ver = "unknown"
		}
		fmt.Fprintf(&sb, "• %s\n    %s · v%s · %s\n", p.Key, status, ver, p.Scope)
	}
	sb.WriteString("\nSee their contributions via /skill, /command, /agent.")
	return r.reply(ctx, chatID, sb.String())
}
