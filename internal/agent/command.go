package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Command is one parsed `.claude/commands/<name>.md` file — the native
// Claude Code slash-command format. The filename (minus .md) is the
// command name; there is no `name:` field in the schema.
//
// Commands are explicitly invoked by the operator (Telegram `/command
// <name>`) and injected as a user-turn preamble. This is distinct from
// native Claude Code **skills** (`.claude/skills/<name>/SKILL.md`), which
// Claude auto-selects based on description — see skill.go.
type Command struct {
	Name         string   // filename sans .md
	Description  string   // from native `description:`
	Body         string   // markdown body — supports the $ARGUMENTS placeholder
	Triggers     []string // optional bot-specific `x-triggers:`
	RequiresRepo bool     // optional bot-specific `x-requires-repo:`

	Source string // absolute path the file was read from
	Scope  string // "global" or "repo"
}

// Commands returns every command visible for the given repo (repoRoot may
// be empty). Repo-local entries override global ones on name collision.
// Sorted by name.
func (l *Loader) Commands(repoRoot string) ([]Command, error) {
	byName := map[string]Command{}

	for _, c := range l.scanCommands(filepath.Join(l.GlobalRoot, ".claude", "commands"), "global") {
		byName[c.Name] = c
	}
	if repoRoot != "" {
		for _, c := range l.scanCommands(filepath.Join(repoRoot, ".claude", "commands"), "repo") {
			byName[c.Name] = c
		}
	}

	out := make([]Command, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Command resolves one command by name against the given repo. Repo-local
// wins. Returns ErrNotFound if absent.
func (l *Loader) Command(repoRoot, name string) (Command, error) {
	cmds, err := l.Commands(repoRoot)
	if err != nil {
		return Command{}, err
	}
	for _, c := range cmds {
		if c.Name == name {
			return c, nil
		}
	}
	return Command{}, ErrNotFound
}

// scanCommands walks dir and returns every valid command under it. Missing
// directory → empty slice (not an error). Malformed files are skipped.
func (l *Loader) scanCommands(dir, scope string) []Command {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Command
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		c, err := parseCommandFile(path)
		if err != nil {
			continue
		}
		c.Scope = scope
		out = append(out, c)
	}
	return out
}

func parseCommandFile(path string) (Command, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Command{}, fmt.Errorf("read %s: %w", path, err)
	}
	frontmatter, body := splitFrontmatter(raw)

	// Native Claude Code command frontmatter uses kebab-case + optional bot
	// extensions prefixed x-.
	var fm struct {
		Description  string   `yaml:"description"`
		ArgumentHint string   `yaml:"argument-hint"`
		AllowedTools []string `yaml:"allowed-tools"`

		XTriggers     []string `yaml:"x-triggers"`
		XRequiresRepo bool     `yaml:"x-requires-repo"`
	}
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
			return Command{}, fmt.Errorf("parse frontmatter %s: %w", path, err)
		}
	}
	_ = fm.ArgumentHint
	_ = fm.AllowedTools

	return Command{
		Name:         strings.TrimSuffix(filepath.Base(path), ".md"),
		Description:  fm.Description,
		Body:         strings.TrimSpace(body),
		Triggers:     fm.XTriggers,
		RequiresRepo: fm.XRequiresRepo,
		Source:       path,
	}, nil
}

// RenderBody returns the command body with $ARGUMENTS substituted. An
// empty args string still substitutes (leaving a literal empty span where
// the placeholder was), matching Claude Code's own behavior.
func (c Command) RenderBody(args string) string {
	if !strings.Contains(c.Body, "$ARGUMENTS") {
		return c.Body
	}
	return strings.ReplaceAll(c.Body, "$ARGUMENTS", args)
}
