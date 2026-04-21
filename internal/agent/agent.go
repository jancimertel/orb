// Package agent loads Claude Code's native `.claude/agents/*.md` files
// (subagent definitions) and exposes them as chat-sticky system prompts
// for the bot.
//
// We honor the native frontmatter schema (name, description, model, tools)
// but ignore `tools` — the bot uses only the body as
// --append-system-prompt content, it does not spawn a sub-agent.
//
// The loader searches two roots and merges them; repo-local files override
// user-global ones on name collision:
//
//	/data/.claude/agents/*.md              (user-global, owner-authored)
//	<active-repo>/.claude/agents/*.md      (per-repo, shipped with the code)
package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Agent is one parsed `.claude/agents/<name>.md` file.
type Agent struct {
	Name        string // from frontmatter `name:`; falls back to filename sans .md
	Description string // one-line `description:` field
	Body        string // markdown body after the frontmatter block

	// Source is the absolute path the file was read from. Used for /agent info
	// and for deciding which scope (global vs. repo-local) a definition came
	// from.
	Source string

	// Scope is "global" or "repo".
	Scope string
}

// Loader resolves agents from the user-global root plus an optional per-chat
// repo-local root. It caches parsed files keyed by absolute path and mtime so
// repeated lookups are cheap. Call Reload to discard the cache.
type Loader struct {
	// GlobalRoot is /data/.claude (the bot's HOME/.claude). The loader reads
	// agents from <GlobalRoot>/agents/*.md.
	GlobalRoot string

	mu    sync.Mutex
	cache map[string]cacheEntry // key: absolute file path
}

type cacheEntry struct {
	mtime int64
	size  int64
	agent Agent
}

// NewLoader builds a Loader rooted at globalRoot (typically cfg.HomeDir).
func NewLoader(globalRoot string) *Loader {
	return &Loader{
		GlobalRoot: globalRoot,
		cache:      map[string]cacheEntry{},
	}
}

// List returns every agent visible for the given repo (repoRoot may be empty
// for "no repo selected"). Repo-local files override global ones on name.
// Results are sorted by name.
func (l *Loader) List(repoRoot string) ([]Agent, error) {
	byName := map[string]Agent{}

	for _, a := range l.scan(filepath.Join(l.GlobalRoot, ".claude", "agents"), "global") {
		byName[a.Name] = a
	}
	if repoRoot != "" {
		for _, a := range l.scan(filepath.Join(repoRoot, ".claude", "agents"), "repo") {
			byName[a.Name] = a
		}
	}

	out := make([]Agent, 0, len(byName))
	for _, a := range byName {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the agent with the given name resolved against the given repo.
// Repo-local wins over global on collision. Returns ErrNotFound if missing.
func (l *Loader) Get(repoRoot, name string) (Agent, error) {
	agents, err := l.List(repoRoot)
	if err != nil {
		return Agent{}, err
	}
	for _, a := range agents {
		if a.Name == name {
			return a, nil
		}
	}
	return Agent{}, ErrNotFound
}

// Reload discards the file cache so the next List/Get re-reads from disk.
func (l *Loader) Reload() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache = map[string]cacheEntry{}
}

// ErrNotFound indicates no agent with the requested name was visible.
var ErrNotFound = errors.New("agent: not found")

// scan walks dir and returns every valid agent under it. A missing directory
// is not an error — callers depend on that because neither root is required.
// Malformed files are skipped silently; a future Validate() surface can
// surface parse errors to the operator.
func (l *Loader) scan(dir, scope string) []Agent {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	var out []Agent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		cached, ok := l.cache[path]
		if ok && cached.mtime == info.ModTime().UnixNano() && cached.size == info.Size() {
			a := cached.agent
			a.Scope = scope
			out = append(out, a)
			continue
		}
		a, err := parseFile(path)
		if err != nil {
			continue
		}
		a.Scope = scope
		l.cache[path] = cacheEntry{
			mtime: info.ModTime().UnixNano(),
			size:  info.Size(),
			agent: a,
		}
		out = append(out, a)
	}
	return out
}

// parseFile reads a single agent markdown file and returns the parsed Agent.
// Returns an error on unreadable files or malformed frontmatter.
func parseFile(path string) (Agent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Agent{}, fmt.Errorf("read %s: %w", path, err)
	}
	frontmatter, body := splitFrontmatter(raw)

	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
			return Agent{}, fmt.Errorf("parse frontmatter %s: %w", path, err)
		}
	}
	name := fm.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	return Agent{
		Name:        name,
		Description: fm.Description,
		Body:        strings.TrimSpace(body),
		Source:      path,
	}, nil
}

// splitFrontmatter peels off a leading `---` … `---` YAML block. If the file
// has no frontmatter (doesn't start with `---\n`), it returns ("", raw).
func splitFrontmatter(raw []byte) (frontmatter []byte, body string) {
	delim := []byte("---")
	trimmed := bytes.TrimLeft(raw, "\ufeff \t")
	if !bytes.HasPrefix(trimmed, delim) {
		return nil, string(raw)
	}
	// Drop the first `---` line.
	after := trimmed[len(delim):]
	if i := bytes.IndexByte(after, '\n'); i >= 0 {
		after = after[i+1:]
	}
	// Find the closing `---` line.
	closeIdx := bytes.Index(after, append([]byte("\n"), delim...))
	if closeIdx < 0 {
		return nil, string(raw)
	}
	frontmatter = after[:closeIdx]
	rest := after[closeIdx+1+len(delim):]
	// Drop the newline immediately after the closing `---`, if any.
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}
	return frontmatter, string(rest)
}
