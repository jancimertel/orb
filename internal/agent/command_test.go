package agent

import (
	"path/filepath"
	"testing"
)

func TestLoader_CommandsRepoOverridesGlobal(t *testing.T) {
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()

	mustWrite(t, filepath.Join(globalRoot, ".claude", "commands", "review-pr.md"),
		"---\ndescription: Global review.\nx-triggers: [review]\n---\nglobal body $ARGUMENTS")
	mustWrite(t, filepath.Join(globalRoot, ".claude", "commands", "explain.md"),
		"---\ndescription: Global explain.\n---\nexplain body")
	mustWrite(t, filepath.Join(repoRoot, ".claude", "commands", "review-pr.md"),
		"---\ndescription: Repo review.\nx-requires-repo: true\n---\nrepo body")

	l := NewLoader(globalRoot)
	cmds, err := l.Commands(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 {
		t.Fatalf("want 2 commands, got %d", len(cmds))
	}
	byName := map[string]Command{}
	for _, c := range cmds {
		byName[c.Name] = c
	}
	if byName["review-pr"].Description != "Repo review." {
		t.Errorf("repo-local should override global; got %q", byName["review-pr"].Description)
	}
	if byName["review-pr"].Scope != "repo" {
		t.Errorf("scope: got %q, want %q", byName["review-pr"].Scope, "repo")
	}
	if !byName["review-pr"].RequiresRepo {
		t.Error("x-requires-repo not parsed")
	}
	if byName["explain"].Scope != "global" {
		t.Errorf("scope: got %q, want %q", byName["explain"].Scope, "global")
	}
}

func TestCommand_RenderBody_ArgumentsSubstitution(t *testing.T) {
	c := Command{Body: "Review diff against $ARGUMENTS and flag issues."}
	got := c.RenderBody("main")
	want := "Review diff against main and flag issues."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// No placeholder → identical.
	c2 := Command{Body: "Plain body."}
	if c2.RenderBody("ignored") != "Plain body." {
		t.Errorf("unexpected rewrite when no placeholder present")
	}
}

func TestLoader_CommandGetNotFound(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.Command("", "nope"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
