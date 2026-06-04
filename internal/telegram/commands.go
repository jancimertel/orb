package telegram

import (
	"context"

	"github.com/mymmrac/telego"
)

// botCommands is the list registered with Telegram's setMyCommands API so
// that typing "/" in the chat shows an autocomplete menu. Keep entries
// short — Telegram truncates descriptions at ~256 chars but the menu UI
// shows much less.
var botCommands = []telego.BotCommand{
	{Command: "help", Description: "show command reference"},
	{Command: "status", Description: "uptime, active repo/session/model"},
	{Command: "usage", Description: "today + MTD token + cost rollups"},
	{Command: "cancel", Description: "abort the running turn"},

	{Command: "new", Description: "start a fresh Claude session"},
	{Command: "session", Description: "browse / resume past sessions"},
	{Command: "model", Description: "list or switch Claude model"},
	{Command: "effort", Description: "set reasoning effort: low|medium|high|xhigh|max"},
	{Command: "agent", Description: "list or switch chat-sticky agent (system prompt)"},
	{Command: "command", Description: "one-shot preamble from .claude/commands/"},
	{Command: "skill", Description: "list native Claude skills from .claude/skills/"},
	{Command: "plugins", Description: "list installed Claude Code plugins"},
	{Command: "remember", Description: "append to user or project memory"},
	{Command: "memory", Description: "show / clear persistent memory scopes"},
	{Command: "context", Description: "show active agent/skill/repo + loader paths"},

	{Command: "repo", Description: "list / add / remove repos"},
	{Command: "cd", Description: "switch active repo"},
	{Command: "pwd", Description: "show active repo + branch"},

	{Command: "diff", Description: "git diff (use --staged for staged)"},
	{Command: "branch", Description: "create + checkout new branch"},
	{Command: "commit", Description: "stage all + commit (with confirm)"},
	{Command: "push", Description: "push current branch (with confirm)"},
	{Command: "pull", Description: "pull --ff-only (with confirm)"},
}

// PublishCommands registers botCommands with Telegram so they appear in the
// chat's autocomplete menu. Call once after the router is wired. Errors are
// non-fatal — the bot still functions, just without the menu.
func (r *Router) PublishCommands(ctx context.Context) error {
	return r.bot.SetMyCommands(ctx, &telego.SetMyCommandsParams{
		Commands: botCommands,
	})
}
