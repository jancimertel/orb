package agent

import (
	"path/filepath"
	"testing"
)

func TestLoader_SkillsScansSkillMd(t *testing.T) {
	globalRoot := t.TempDir()

	mustWrite(t, filepath.Join(globalRoot, ".claude", "skills", "commit", "SKILL.md"),
		"---\nname: commit\ndescription: Making a clean commit.\n---\nGuidance body.")
	mustWrite(t, filepath.Join(globalRoot, ".claude", "skills", "reducing-entropy", "SKILL.md"),
		"---\nname: reducing-entropy\ndescription: Shrink codebase.\n---\nBody.")
	// Dir without SKILL.md — must be skipped.
	mustWrite(t, filepath.Join(globalRoot, ".claude", "skills", "bare", "README.md"), "no SKILL.md here")

	l := NewLoader(globalRoot)
	got, err := l.Skills("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 skills, got %d: %+v", len(got), got)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if byName["commit"].Description != "Making a clean commit." {
		t.Errorf("description: got %q", byName["commit"].Description)
	}
	if byName["commit"].Scope != "global" {
		t.Errorf("scope: got %q", byName["commit"].Scope)
	}
}

func TestLoader_SkillsRepoOverridesGlobal(t *testing.T) {
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()

	mustWrite(t, filepath.Join(globalRoot, ".claude", "skills", "commit", "SKILL.md"),
		"---\nname: commit\ndescription: Global.\n---\nglobal body")
	mustWrite(t, filepath.Join(repoRoot, ".claude", "skills", "commit", "SKILL.md"),
		"---\nname: commit\ndescription: Repo.\n---\nrepo body")

	l := NewLoader(globalRoot)
	got, err := l.Skills(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Description != "Repo." || got[0].Scope != "repo" {
		t.Errorf("repo override failed: %+v", got)
	}
}

func TestLoader_SkillNotFound(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.Skill("", "nope"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestLoader_SkillDirNameOverridesFrontmatterName(t *testing.T) {
	globalRoot := t.TempDir()
	// Frontmatter says "x"; directory is "y". Directory must win so operators
	// can't accidentally shadow each other by typo.
	mustWrite(t, filepath.Join(globalRoot, ".claude", "skills", "y", "SKILL.md"),
		"---\nname: x\ndescription: d\n---\nbody")
	l := NewLoader(globalRoot)
	got, err := l.Skills("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "y" {
		t.Errorf("want name=y (dir), got %+v", got)
	}
}
