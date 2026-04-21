package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFile_Frontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brief.md")
	if err := os.WriteFile(path, []byte(`---
name: brief
description: Ultra-terse replies.
model: inherit
---

Respond in ≤3 sentences.
Cite file:line for all references.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if a.Name != "brief" {
		t.Errorf("name: got %q, want %q", a.Name, "brief")
	}
	if a.Description != "Ultra-terse replies." {
		t.Errorf("description: got %q", a.Description)
	}
	if a.Body != "Respond in ≤3 sentences.\nCite file:line for all references." {
		t.Errorf("body: got %q", a.Body)
	}
}

func TestParseFile_NoFrontmatter_UsesFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rude.md")
	if err := os.WriteFile(path, []byte("Just body, no frontmatter.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if a.Name != "rude" {
		t.Errorf("name: got %q, want %q", a.Name, "rude")
	}
	if a.Body != "Just body, no frontmatter." {
		t.Errorf("body: got %q", a.Body)
	}
}

func TestLoader_RepoOverridesGlobal(t *testing.T) {
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()

	mustWrite(t, filepath.Join(globalRoot, ".claude", "agents", "brief.md"),
		"---\nname: brief\ndescription: Global brief.\n---\nglobal body")
	mustWrite(t, filepath.Join(globalRoot, ".claude", "agents", "rude.md"),
		"---\nname: rude\ndescription: Global rude.\n---\nglobal body")
	mustWrite(t, filepath.Join(repoRoot, ".claude", "agents", "brief.md"),
		"---\nname: brief\ndescription: Repo brief.\n---\nrepo body")

	l := NewLoader(globalRoot)
	agents, err := l.List(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("want 2 agents, got %d", len(agents))
	}
	byName := map[string]Agent{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	if byName["brief"].Description != "Repo brief." {
		t.Errorf("repo-local should override global; got %q", byName["brief"].Description)
	}
	if byName["brief"].Scope != "repo" {
		t.Errorf("scope: got %q, want %q", byName["brief"].Scope, "repo")
	}
	if byName["rude"].Scope != "global" {
		t.Errorf("scope: got %q, want %q", byName["rude"].Scope, "global")
	}
}

func TestLoader_MissingDirsAreFine(t *testing.T) {
	l := NewLoader(t.TempDir())
	agents, err := l.List(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Errorf("want 0, got %d", len(agents))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
