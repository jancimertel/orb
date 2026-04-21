package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is one parsed `.claude/skills/<name>/SKILL.md` file — the native
// Claude Code skill format. Skills are **auto-discovered** by Claude
// inside the subprocess based on their description; the bot does not
// inject or invoke them. This type exists only so the Telegram UI can
// list / inspect what's installed, for operator visibility.
//
// Layout:
//
//	.claude/skills/
//	  commit/
//	    SKILL.md         ← frontmatter: name, description, …
//	    references/...   ← arbitrary supporting files (we ignore them)
//
// The directory name is authoritative for the skill's identity; the
// `name:` frontmatter field is informational. Missing SKILL.md = skip
// the directory silently.
type Skill struct {
	Name        string // directory name (authoritative) — falls back to `name:` if they differ
	Description string // from frontmatter `description:`
	Body        string // markdown body after the frontmatter
	Source      string // absolute path to SKILL.md
	Scope       string // "global" or "repo"
}

// Skills returns every skill visible for the given repo (repoRoot may be
// empty). Repo-local entries override global ones on name collision.
// Sorted by name.
func (l *Loader) Skills(repoRoot string) ([]Skill, error) {
	byName := map[string]Skill{}

	for _, s := range l.scanSkills(filepath.Join(l.GlobalRoot, ".claude", "skills"), "global") {
		byName[s.Name] = s
	}
	if repoRoot != "" {
		for _, s := range l.scanSkills(filepath.Join(repoRoot, ".claude", "skills"), "repo") {
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

// Skill resolves one skill by directory name against the given repo.
// Repo-local wins. Returns ErrNotFound if absent.
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

// scanSkills walks dir looking for `<skill>/SKILL.md` entries. Missing dir
// → empty slice (not an error). Malformed files / missing SKILL.md are
// skipped silently.
func (l *Loader) scanSkills(dir, scope string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		s, err := parseSkillFile(path)
		if err != nil {
			continue
		}
		// Directory name is authoritative; frontmatter `name` is informational.
		s.Name = e.Name()
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

	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
			return Skill{}, fmt.Errorf("parse frontmatter %s: %w", path, err)
		}
	}
	return Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Body:        strings.TrimSpace(body),
		Source:      path,
	}, nil
}
