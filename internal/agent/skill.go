package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is one parsed `.claude/commands/<name>.md` file. The filename (minus
// .md) is the command name — there is no `name:` field in the native Claude
// Code command schema.
type Skill struct {
	Name        string   // filename sans .md
	Description string   // from native `description:`
	Body        string   // markdown body — supports the $ARGUMENTS placeholder
	Triggers    []string // optional bot-specific `x-triggers:`
	RequiresRepo bool    // optional bot-specific `x-requires-repo:`

	Source string // absolute path the file was read from
	Scope  string // "global" or "repo"
}

// Skills returns every skill visible for the given repo (repoRoot may be
// empty). Repo-local entries override global ones on name collision. Sorted.
func (l *Loader) Skills(repoRoot string) ([]Skill, error) {
	byName := map[string]Skill{}

	for _, s := range l.scanSkills(filepath.Join(l.GlobalRoot, ".claude", "commands"), "global") {
		byName[s.Name] = s
	}
	if repoRoot != "" {
		for _, s := range l.scanSkills(filepath.Join(repoRoot, ".claude", "commands"), "repo") {
			byName[s.Name] = s
		}
	}

	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Skill resolves one skill by name against the given repo. Repo-local wins.
// Returns ErrNotFound if absent.
func (l *Loader) Skill(repoRoot, name string) (Skill, error) {
	skills, err := l.Skills(repoRoot)
	if err != nil {
		return Skill{}, err
	}
	for _, s := range skills {
		if s.Name == name {
			return s, nil
		}
	}
	return Skill{}, ErrNotFound
}

// scanSkills walks dir and returns every valid skill under it. Missing
// directory → empty slice (not an error). Malformed files are skipped.
func (l *Loader) scanSkills(dir, scope string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		s, err := parseSkillFile(path)
		if err != nil {
			continue
		}
		s.Scope = scope
		out = append(out, s)
	}
	return out
}

func parseSkillFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read %s: %w", path, err)
	}
	frontmatter, body := splitFrontmatter(raw)

	// Native Claude Code command frontmatter uses kebab-case + optional bot
	// extensions prefixed x-.
	var fm struct {
		Description  string   `yaml:"description"`
		ArgumentHint string   `yaml:"argument-hint"`
		AllowedTools []string `yaml:"allowed-tools"`

		XTriggers    []string `yaml:"x-triggers"`
		XRequiresRepo bool    `yaml:"x-requires-repo"`
	}
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
			return Skill{}, fmt.Errorf("parse frontmatter %s: %w", path, err)
		}
	}
	_ = fm.ArgumentHint
	_ = fm.AllowedTools

	return Skill{
		Name:         strings.TrimSuffix(filepath.Base(path), ".md"),
		Description:  fm.Description,
		Body:         strings.TrimSpace(body),
		Triggers:     fm.XTriggers,
		RequiresRepo: fm.XRequiresRepo,
		Source:       path,
	}, nil
}

// RenderBody returns the skill body with $ARGUMENTS substituted. An empty
// args string still substitutes (leaving a literal empty span where the
// placeholder was), matching Claude Code's own behavior.
func (s Skill) RenderBody(args string) string {
	if !strings.Contains(s.Body, "$ARGUMENTS") {
		return s.Body
	}
	return strings.ReplaceAll(s.Body, "$ARGUMENTS", args)
}
