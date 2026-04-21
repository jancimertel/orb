package telegram

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/agent"
)

// registerMemory wires /remember and /memory. Memory is markdown on disk
// under .claude/memory/{user,project}.md; the bot appends entries, lists
// contents, and injects them into the first turn of each fresh session.
func (r *Router) registerMemory(h *th.BotHandler) {
	h.Handle(r.handleRemember, th.CommandEqual("remember"))
	h.Handle(r.handleMemory, th.CommandEqual("memory"))
	h.Handle(r.handleMemoryCallback, th.CallbackDataPrefix(memoryPrefix))
}

const (
	memoryPrefix         = "mem:"
	memoryActionClear    = "clear"
	memoryActionConfirm  = "clearok"
	memoryActionCancel   = "cancel"
	memoryConfirmTTL     = 60 * time.Second
)

// pendingClears holds "clear user" / "clear project" confirmations per chat,
// keyed by chat id + scope. Single-shot: consumed on confirm or TTL.
type memoryConfirmStore struct {
	mu  sync.Mutex
	m   map[memoryConfirmKey]time.Time
	ttl time.Duration
}

type memoryConfirmKey struct {
	chatID int64
	scope  string
}

func newMemoryConfirmStore(ttl time.Duration) *memoryConfirmStore {
	return &memoryConfirmStore{m: map[memoryConfirmKey]time.Time{}, ttl: ttl}
}

func (s *memoryConfirmStore) Arm(chatID int64, scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[memoryConfirmKey{chatID, scope}] = time.Now().Add(s.ttl)
}

func (s *memoryConfirmStore) Take(chatID int64, scope string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := memoryConfirmKey{chatID, scope}
	deadline, ok := s.m[k]
	delete(s.m, k)
	return ok && time.Now().Before(deadline)
}

// handleRemember appends an entry to a memory scope.
//
// Usage:
//
//	/remember <text>            → default scope (project if repo active, else user)
//	/remember user <text>       → force user scope
//	/remember project <text>    → force project scope (requires active repo)
func (r *Router) handleRemember(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	raw := strings.TrimSpace(argAfterCommand(u.Message.Text))
	if raw == "" {
		return r.reply(ctx, chatID,
			"usage: /remember [user|project] <text>\n"+
				"  no scope = project if a repo is active, else user\n"+
				"example: /remember integration tests must hit real postgres")
	}

	scope, text := parseRememberArgs(raw, r.activeRepoRoot(ctx, chatID) != "")
	if text == "" {
		return r.reply(ctx, chatID, "nothing to remember — provide text after the scope")
	}

	repoRoot := r.activeRepoRoot(ctx, chatID)
	m, err := r.agents.AppendMemory(repoRoot, scope, text)
	if err != nil {
		if errors.Is(err, agent.ErrNoRepo) {
			return r.reply(ctx, chatID,
				"project memory needs an active repo — use /cd <alias> first, "+
					"or /remember user <text> to save globally")
		}
		if errors.Is(err, agent.ErrBadScope) {
			return r.reply(ctx, chatID, "unknown scope — use user or project")
		}
		r.logger.Error("remember failed", "chat_id", chatID, "scope", scope, "err", err)
		return r.reply(ctx, chatID, "failed to write memory — check logs")
	}

	return r.reply(ctx, chatID, fmt.Sprintf(
		"remembered (%s).\n%s\n\nApplies from the next fresh session — "+
			"use /new to start one, or it applies automatically after /agent or /model.",
		scope, m.Path))
}

// parseRememberArgs decodes the argument portion of /remember. When the
// first token is "user" or "project", that's the explicit scope; otherwise
// everything is the entry text and the scope falls back to the default
// (project if repo is active, user otherwise).
func parseRememberArgs(raw string, repoActive bool) (scope, text string) {
	head, rest, _ := strings.Cut(raw, " ")
	switch head {
	case agent.ScopeUser, agent.ScopeProject:
		return head, strings.TrimSpace(rest)
	}
	if repoActive {
		return agent.ScopeProject, raw
	}
	return agent.ScopeUser, raw
}

// handleMemory shows / inspects / clears memory.
//
// Usage:
//
//	/memory                → summary of both scopes (size, entry count, path)
//	/memory show [scope]   → full contents of a scope (or both if omitted)
//	/memory clear <scope>  → two-step clear with inline confirm
//	/memory path           → print file paths so the operator can edit directly
func (r *Router) handleMemory(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	arg := strings.TrimSpace(argAfterCommand(u.Message.Text))
	head, rest, _ := strings.Cut(arg, " ")

	switch head {
	case "":
		return r.showMemorySummary(ctx, chatID)
	case "show":
		return r.showMemoryContents(ctx, chatID, strings.TrimSpace(rest))
	case "clear":
		return r.armMemoryClear(ctx, chatID, strings.TrimSpace(rest))
	case "path":
		return r.showMemoryPaths(ctx, chatID)
	default:
		return r.reply(ctx, chatID,
			"usage: /memory | /memory show [user|project] | "+
				"/memory clear <user|project> | /memory path")
	}
}

func (r *Router) showMemorySummary(ctx *th.Context, chatID int64) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	var sb strings.Builder

	for _, scope := range []string{agent.ScopeUser, agent.ScopeProject} {
		m, err := r.agents.Memory(repoRoot, scope)
		fmt.Fprintf(&sb, "=== %s ===\n", scope)
		if err != nil {
			if errors.Is(err, agent.ErrNoRepo) {
				sb.WriteString("  (no active repo — select one to see project memory)\n\n")
				continue
			}
			fmt.Fprintf(&sb, "  error: %v\n\n", err)
			continue
		}
		fmt.Fprintf(&sb, "  path: %s\n", m.Path)
		if !m.Exists {
			sb.WriteString("  (empty — use /remember to add an entry)\n\n")
			continue
		}
		entries := countBullets(m.Body)
		fmt.Fprintf(&sb, "  entries: %d  |  bytes: %d\n", entries, len(m.Body))
		preview := strings.TrimSpace(m.Body)
		if len(preview) > 400 {
			preview = preview[:400] + "…"
		}
		sb.WriteString("\n")
		sb.WriteString(preview)
		sb.WriteString("\n\n")
	}

	sb.WriteString("commands: /remember <text> · /memory show · /memory clear <scope>")
	return r.reply(ctx, chatID, sb.String())
}

func (r *Router) showMemoryContents(ctx *th.Context, chatID int64, scope string) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	scopes := []string{agent.ScopeUser, agent.ScopeProject}
	if scope != "" {
		if scope != agent.ScopeUser && scope != agent.ScopeProject {
			return r.reply(ctx, chatID, "unknown scope — use user or project")
		}
		scopes = []string{scope}
	}

	var sb strings.Builder
	for _, s := range scopes {
		m, err := r.agents.Memory(repoRoot, s)
		if err != nil {
			if errors.Is(err, agent.ErrNoRepo) {
				if scope != "" {
					return r.reply(ctx, chatID,
						"project memory needs an active repo — use /cd <alias> first")
				}
				continue
			}
			fmt.Fprintf(&sb, "=== %s ===\n  error: %v\n\n", s, err)
			continue
		}
		fmt.Fprintf(&sb, "=== %s (%s) ===\n", s, m.Path)
		if !m.Exists {
			sb.WriteString("(empty)\n\n")
			continue
		}
		sb.WriteString(m.Body)
		if !strings.HasSuffix(m.Body, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}

	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		out = "(no memory yet)"
	}
	for _, chunk := range chunkMessage(out, 3900) {
		if err := r.reply(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) armMemoryClear(ctx *th.Context, chatID int64, scope string) error {
	if scope == "" {
		return r.reply(ctx, chatID, "usage: /memory clear <user|project>")
	}
	if scope != agent.ScopeUser && scope != agent.ScopeProject {
		return r.reply(ctx, chatID, "unknown scope — use user or project")
	}
	if scope == agent.ScopeProject && r.activeRepoRoot(ctx, chatID) == "" {
		return r.reply(ctx, chatID, "project memory needs an active repo")
	}
	r.memoryConfirms.Arm(chatID, scope)
	rows := [][]telego.InlineKeyboardButton{{
		{Text: "Confirm clear", CallbackData: memoryPrefix + memoryActionConfirm + ":" + scope},
		{Text: "Cancel", CallbackData: memoryPrefix + memoryActionCancel + ":" + scope},
	}}
	_, err := r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        fmt.Sprintf("clear %s memory? (deletes the file on disk)", scope),
		ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	return err
}

func (r *Router) showMemoryPaths(ctx *th.Context, chatID int64) error {
	repoRoot := r.activeRepoRoot(ctx, chatID)
	var sb strings.Builder
	sb.WriteString("Edit these files directly on disk; changes apply to fresh sessions only.\n\n")
	for _, scope := range []string{agent.ScopeUser, agent.ScopeProject} {
		path, err := r.agents.MemoryPath(repoRoot, scope)
		if err != nil {
			if errors.Is(err, agent.ErrNoRepo) {
				fmt.Fprintf(&sb, "%s: (select a repo to see path)\n", scope)
				continue
			}
			fmt.Fprintf(&sb, "%s: error: %v\n", scope, err)
			continue
		}
		fmt.Fprintf(&sb, "%s: %s\n", scope, path)
	}
	return r.reply(ctx, chatID, sb.String())
}

func (r *Router) handleMemoryCallback(ctx *th.Context, u telego.Update) error {
	cq := u.CallbackQuery
	if cq == nil {
		return nil
	}
	rest := strings.TrimPrefix(cq.Data, memoryPrefix)
	action, scope, ok := strings.Cut(rest, ":")
	if !ok || scope == "" {
		return r.answerCallback(ctx, cq.ID, "malformed")
	}
	chatID := callbackChatID(cq)

	switch action {
	case memoryActionCancel:
		_ = r.answerCallback(ctx, cq.ID, "cancelled")
		if cq.Message != nil {
			_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
				ChatID:    telego.ChatID{ID: chatID},
				MessageID: cq.Message.GetMessageID(),
				Text:      "clear cancelled",
			})
		}
		return nil
	case memoryActionConfirm:
		if !r.memoryConfirms.Take(chatID, scope) {
			_ = r.answerCallback(ctx, cq.ID, "expired")
			if cq.Message != nil {
				_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
					ChatID:    telego.ChatID{ID: chatID},
					MessageID: cq.Message.GetMessageID(),
					Text:      "confirmation expired — re-run /memory clear",
				})
			}
			return nil
		}
		repoRoot := r.activeRepoRoot(ctx, chatID)
		if err := r.agents.ClearMemory(repoRoot, scope); err != nil {
			r.logger.Error("clear memory failed", "chat_id", chatID, "scope", scope, "err", err)
			_ = r.answerCallback(ctx, cq.ID, "failed")
			return r.reply(ctx, chatID, "failed to clear memory — check logs")
		}
		_ = r.answerCallback(ctx, cq.ID, "cleared")
		if cq.Message != nil {
			_, _ = r.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
				ChatID:    telego.ChatID{ID: chatID},
				MessageID: cq.Message.GetMessageID(),
				Text:      fmt.Sprintf("%s memory cleared", scope),
			})
		}
		return nil
	case memoryActionClear:
		// Legacy prefix kept only so stale inline keyboards don't 500.
		return r.answerCallback(ctx, cq.ID, "re-run /memory clear")
	default:
		return r.answerCallback(ctx, cq.ID, "unknown action")
	}
}

// countBullets counts lines that start with "- " after stripping leading
// whitespace. Good enough for the summary — we don't need a markdown
// parser, and operators can still edit the file freely.
func countBullets(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "- ") {
			n++
		}
	}
	return n
}
