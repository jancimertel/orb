package telegram

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/agent"
)

// registerContext wires /context. This is a diagnostic view of everything
// the bot injects into the next Claude turn: active agent + body, armed
// skill, model, repo, session, and the file paths the loader will consult.
// Use this when an agent "doesn't seem to apply" — if the body isn't
// printed here, it isn't being sent.
func (r *Router) registerContext(h *th.BotHandler) {
	h.Handle(r.handleContext, th.CommandEqual("context"))
}

func (r *Router) handleContext(ctx *th.Context, u telego.Update) error {
	if u.Message == nil {
		return nil
	}
	chatID := u.Message.Chat.ID
	cs, _ := r.chatStateOrEmpty(ctx, chatID)

	var sb strings.Builder

	// --- Model ---
	fmt.Fprintf(&sb, "Model:   %s\n", r.activeModelFor(ctx, chatID))

	// --- Repo ---
	repoRoot := r.activeRepoRoot(ctx, chatID)
	if repoRoot == "" {
		sb.WriteString("Repo:    (none)\n")
	} else {
		fmt.Fprintf(&sb, "Repo:    %s (%s)\n", cs.ActiveRepoAlias, repoRoot)
	}

	// --- Session ---
	if cs.ActiveSessionID == "" {
		sb.WriteString("Session: (fresh — next turn starts a new one)\n")
	} else {
		fmt.Fprintf(&sb, "Session: %s\n", cs.ActiveSessionID)
	}

	// --- Agent ---
	agentName := r.activeAgentName(ctx, chatID)
	if agentName == "" {
		sb.WriteString("\nAgent:   (none)\n")
	} else if a, err := r.agents.Get(repoRoot, agentName); err == nil {
		fmt.Fprintf(&sb, "\nAgent:   %s (%s)\n", a.Name, a.Scope)
		fmt.Fprintf(&sb, "Source:  %s\n", a.Source)
		fmt.Fprintf(&sb, "Applied as: %s\n",
			ternary(cs.ActiveSessionID == "", "user-message preamble on this turn", "baked into session history"))
		sb.WriteString("\n--- agent body ---\n")
		sb.WriteString(a.Body)
		sb.WriteString("\n--- end body ---\n")
	} else {
		fmt.Fprintf(&sb, "\nAgent:   %s (not found on disk — did you delete the file?)\n", agentName)
	}

	// --- Armed command ---
	if armed := r.pendingCommands.Peek(chatID); armed != "" {
		fmt.Fprintf(&sb, "\nArmed command: %s — applies to your next text message\n", armed)
	}

	// --- Memory ---
	sb.WriteString("\n--- memory ---\n")
	for _, scope := range []string{agent.ScopeUser, agent.ScopeProject} {
		m, err := r.agents.Memory(repoRoot, scope)
		if err != nil {
			if errors.Is(err, agent.ErrNoRepo) {
				fmt.Fprintf(&sb, "  %s: (no active repo)\n", scope)
				continue
			}
			fmt.Fprintf(&sb, "  %s: error: %v\n", scope, err)
			continue
		}
		if !m.Exists {
			fmt.Fprintf(&sb, "  %s: (empty)  %s\n", scope, m.Path)
			continue
		}
		fmt.Fprintf(&sb, "  %s: %d bytes, %d entries  %s\n",
			scope, len(m.Body), countBullets(m.Body), m.Path)
	}
	preamble := r.agents.MemoryPreamble(repoRoot)
	if preamble == "" {
		sb.WriteString("  (nothing to inject)\n")
	} else {
		fmt.Fprintf(&sb, "  Applied as: %s\n",
			ternary(cs.ActiveSessionID == "", "user-message preamble on this turn",
				"baked into session history (new entries need /new)"))
	}

	// --- Loader search paths ---
	sb.WriteString("\n--- loader search paths ---\n")
	fmt.Fprintf(&sb, "Global agents:   %s\n", filepath.Join(r.cfg.HomeDir, ".claude", "agents"))
	fmt.Fprintf(&sb, "Global commands: %s\n", filepath.Join(r.cfg.HomeDir, ".claude", "commands"))
	fmt.Fprintf(&sb, "Global skills:   %s\n", filepath.Join(r.cfg.HomeDir, ".claude", "skills"))
	fmt.Fprintf(&sb, "Global jobs:     %s\n", filepath.Join(r.cfg.HomeDir, ".claude", "jobs"))
	if repoRoot != "" {
		fmt.Fprintf(&sb, "Repo agents:     %s\n", filepath.Join(repoRoot, ".claude", "agents"))
		fmt.Fprintf(&sb, "Repo commands:   %s\n", filepath.Join(repoRoot, ".claude", "commands"))
		fmt.Fprintf(&sb, "Repo skills:     %s\n", filepath.Join(repoRoot, ".claude", "skills"))
		fmt.Fprintf(&sb, "Repo jobs:       %s\n", filepath.Join(repoRoot, ".claude", "jobs"))
	}

	// --- What's actually on disk now ---
	sb.WriteString("\n--- installed agents ---\n")
	if agents, err := r.agents.List(repoRoot); err == nil && len(agents) > 0 {
		for _, a := range agents {
			active := ""
			if a.Name == agentName {
				active = "  ← active"
			}
			fmt.Fprintf(&sb, "  %s  (%s)%s\n", a.Name, a.Scope, active)
		}
	} else {
		sb.WriteString("  (none)\n")
	}

	sb.WriteString("\n--- installed commands ---\n")
	if cmds, err := r.agents.Commands(repoRoot); err == nil && len(cmds) > 0 {
		for _, c := range cmds {
			fmt.Fprintf(&sb, "  %s  (%s)\n", c.Name, c.Scope)
		}
	} else {
		sb.WriteString("  (none)\n")
	}

	sb.WriteString("\n--- installed skills (auto-discovered) ---\n")
	if skills, err := r.agents.Skills(repoRoot); err == nil && len(skills) > 0 {
		for _, s := range skills {
			fmt.Fprintf(&sb, "  %s  (%s)\n", s.Name, s.Scope)
		}
	} else {
		sb.WriteString("  (none)\n")
	}

	// Telegram caps at 4096 chars per message; chunker handles long bodies.
	for _, chunk := range chunkMessage(sb.String(), 3900) {
		if err := r.reply(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
