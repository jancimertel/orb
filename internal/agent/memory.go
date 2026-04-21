package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Memory is a single persistent note-file the operator curates via
// /remember and /memory. It is markdown on disk, appended to by the bot,
// and injected as a user-turn preamble on fresh sessions (same mechanism
// as agent bodies) so Claude sees it at the top of the conversation.
//
// Two scopes exist:
//
//   - user:    $HOME_DIR/.claude/memory/user.md        — global, every chat
//   - project: <repo>/.claude/memory/project.md        — per-repo, shared
//
// Both files live inside `.claude/memory/` so they travel with their
// natural home: the user scope lives on the persistent volume alongside
// agents/commands/jobs; the project scope lives in the repo, so cloning
// the repo elsewhere (or opening it in Claude Code directly) picks it up.
type Memory struct {
	Scope  string // "user" | "project"
	Path   string // absolute file path
	Body   string // file contents, empty string if file is absent
	Exists bool   // true if the file is on disk
}

// MemoryScope values.
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

// ErrNoRepo is returned when a project-scoped memory op is requested but
// no repo is active. Callers surface this to the operator verbatim.
var ErrNoRepo = errors.New("memory: no active repo — select one first")

// ErrBadScope is returned when a scope name other than user/project is used.
var ErrBadScope = errors.New("memory: unknown scope (want user|project)")

// MemoryPath returns the absolute path where a given scope's file lives.
// repoRoot is required for scope=project and ignored for scope=user.
// Errors on unknown scope or project-without-repo.
func (l *Loader) MemoryPath(repoRoot, scope string) (string, error) {
	switch scope {
	case ScopeUser:
		return filepath.Join(l.GlobalRoot, ".claude", "memory", "user.md"), nil
	case ScopeProject:
		if repoRoot == "" {
			return "", ErrNoRepo
		}
		return filepath.Join(repoRoot, ".claude", "memory", "project.md"), nil
	default:
		return "", ErrBadScope
	}
}

// Memory loads a scope's file. A missing file is not an error — returns a
// Memory with Exists=false and an empty Body, so callers can treat "no
// memory" and "empty memory" the same way.
func (l *Loader) Memory(repoRoot, scope string) (Memory, error) {
	path, err := l.MemoryPath(repoRoot, scope)
	if err != nil {
		return Memory{}, err
	}
	m := Memory{Scope: scope, Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return m, fmt.Errorf("memory: read %s: %w", path, err)
	}
	m.Exists = true
	m.Body = string(data)
	return m, nil
}

// AppendMemory appends a single entry to a scope. Creates the file (and its
// parent directory) on first write, and seeds a minimal header if new so
// the file is self-describing if an operator opens it directly.
//
// The entry is written as a dated bullet:
//
//	- 2026-04-21: prefer terse responses
//
// Multi-line entries are preserved with continuation indentation so they
// render as one bullet in markdown viewers.
func (l *Loader) AppendMemory(repoRoot, scope, entry string) (Memory, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return Memory{}, errors.New("memory: empty entry")
	}
	path, err := l.MemoryPath(repoRoot, scope)
	if err != nil {
		return Memory{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Memory{}, fmt.Errorf("memory: mkdir: %w", err)
	}

	existing, err := os.ReadFile(path)
	fresh := false
	if err != nil {
		if !os.IsNotExist(err) {
			return Memory{}, fmt.Errorf("memory: read %s: %w", path, err)
		}
		fresh = true
		existing = nil
	}

	var sb strings.Builder
	if fresh {
		fmt.Fprintf(&sb, "# Memory (scope: %s)\n\n", scope)
	} else {
		sb.Write(existing)
		// Ensure the file ends with a newline before the new entry so the
		// bullet lands on its own line even if the operator edited the file
		// by hand and forgot the trailing newline.
		if len(existing) > 0 && existing[len(existing)-1] != '\n' {
			sb.WriteByte('\n')
		}
	}

	fmt.Fprintf(&sb, "- %s: %s\n", time.Now().UTC().Format("2006-01-02"), indentContinuation(entry))

	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return Memory{}, fmt.Errorf("memory: write %s: %w", path, err)
	}
	return Memory{Scope: scope, Path: path, Body: sb.String(), Exists: true}, nil
}

// ClearMemory deletes the scope's file. A missing file is a no-op so the
// operator can call it safely without checking first.
func (l *Loader) ClearMemory(repoRoot, scope string) error {
	path, err := l.MemoryPath(repoRoot, scope)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: remove %s: %w", path, err)
	}
	return nil
}

// MemoryPreamble returns the block injected into the first turn of a fresh
// session when at least one scope has content. The format is a single
// <memory> element per scope, in scope-name order, so Claude sees the
// structure without us having to teach it a new convention.
//
// Empty string means nothing to inject — callers should skip the preamble.
func (l *Loader) MemoryPreamble(repoRoot string) string {
	scopes := []string{ScopeUser, ScopeProject}
	var sb strings.Builder
	for _, scope := range scopes {
		m, err := l.Memory(repoRoot, scope)
		if err != nil || !m.Exists {
			continue
		}
		body := strings.TrimSpace(m.Body)
		if body == "" {
			continue
		}
		fmt.Fprintf(&sb, "<memory scope=%q>\n%s\n</memory>\n", scope, body)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// indentContinuation turns any internal newline into `\n  ` so a multi-line
// entry stays visually attached to its bullet in markdown. We don't bother
// escaping markdown special chars — the operator is trusted content and
// the file is primarily machine-read by Claude.
func indentContinuation(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	return strings.ReplaceAll(s, "\n", "\n  ")
}
