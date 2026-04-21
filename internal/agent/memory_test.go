package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryPath_UserAndProject(t *testing.T) {
	l := NewLoader("/data")
	got, err := l.MemoryPath("", ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/data", ".claude", "memory", "user.md")
	if got != want {
		t.Errorf("user path: got %q want %q", got, want)
	}

	got, err = l.MemoryPath("/repo", ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join("/repo", ".claude", "memory", "project.md")
	if got != want {
		t.Errorf("project path: got %q want %q", got, want)
	}
}

func TestMemoryPath_ErrNoRepoForProject(t *testing.T) {
	l := NewLoader("/data")
	_, err := l.MemoryPath("", ScopeProject)
	if !errors.Is(err, ErrNoRepo) {
		t.Errorf("want ErrNoRepo, got %v", err)
	}
}

func TestMemoryPath_BadScope(t *testing.T) {
	l := NewLoader("/data")
	_, err := l.MemoryPath("", "chat")
	if !errors.Is(err, ErrBadScope) {
		t.Errorf("want ErrBadScope, got %v", err)
	}
}

func TestMemory_MissingFileReturnsExistsFalse(t *testing.T) {
	l := NewLoader(t.TempDir())
	m, err := l.Memory("", ScopeUser)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if m.Exists {
		t.Errorf("Exists: want false")
	}
	if m.Body != "" {
		t.Errorf("Body: want empty, got %q", m.Body)
	}
	if m.Scope != ScopeUser {
		t.Errorf("Scope: got %q", m.Scope)
	}
}

func TestAppendMemory_CreatesFileWithHeader(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory("", ScopeUser, "prefer terse replies"); err != nil {
		t.Fatalf("append: %v", err)
	}
	m, err := l.Memory("", ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Exists {
		t.Fatal("file should exist")
	}
	if !strings.Contains(m.Body, "# Memory (scope: user)") {
		t.Errorf("missing header, body=%q", m.Body)
	}
	if !strings.Contains(m.Body, "prefer terse replies") {
		t.Errorf("missing entry, body=%q", m.Body)
	}
	if !strings.Contains(m.Body, "- ") {
		t.Errorf("entry not bulleted, body=%q", m.Body)
	}
}

func TestAppendMemory_PreservesExistingOnSecondCall(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory("", ScopeUser, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendMemory("", ScopeUser, "second"); err != nil {
		t.Fatal(err)
	}
	m, _ := l.Memory("", ScopeUser)
	if strings.Count(m.Body, "- ") != 2 {
		t.Errorf("want 2 bullets, got body=%q", m.Body)
	}
	// No header duplication.
	if n := strings.Count(m.Body, "# Memory"); n != 1 {
		t.Errorf("want 1 header, got %d: %q", n, m.Body)
	}
}

func TestAppendMemory_RejectsEmpty(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory("", ScopeUser, "   "); err == nil {
		t.Error("want error on empty entry")
	}
}

func TestAppendMemory_MultiLineContinuation(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory("", ScopeUser, "first line\nsecond line\nthird"); err != nil {
		t.Fatal(err)
	}
	m, _ := l.Memory("", ScopeUser)
	if !strings.Contains(m.Body, "first line\n  second line\n  third") {
		t.Errorf("continuation not indented, body=%q", m.Body)
	}
}

func TestAppendMemory_ProjectRequiresRepo(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory("", ScopeProject, "x"); !errors.Is(err, ErrNoRepo) {
		t.Errorf("want ErrNoRepo, got %v", err)
	}
}

func TestAppendMemory_ProjectWritesIntoRepo(t *testing.T) {
	repo := t.TempDir()
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory(repo, ScopeProject, "use real postgres"); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(repo, ".claude", "memory", "project.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file missing: %v", err)
	}
	if !strings.Contains(string(data), "use real postgres") {
		t.Errorf("wrong content: %q", data)
	}
}

func TestClearMemory_RemovesFile(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory("", ScopeUser, "x"); err != nil {
		t.Fatal(err)
	}
	if err := l.ClearMemory("", ScopeUser); err != nil {
		t.Fatalf("clear: %v", err)
	}
	m, _ := l.Memory("", ScopeUser)
	if m.Exists {
		t.Error("file should be gone")
	}
}

func TestClearMemory_MissingIsNoOp(t *testing.T) {
	l := NewLoader(t.TempDir())
	if err := l.ClearMemory("", ScopeUser); err != nil {
		t.Errorf("missing should be a no-op, got %v", err)
	}
}

func TestMemoryPreamble_EmitsScopesInOrder(t *testing.T) {
	globalRoot := t.TempDir()
	repo := t.TempDir()
	l := NewLoader(globalRoot)
	if _, err := l.AppendMemory("", ScopeUser, "u entry"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendMemory(repo, ScopeProject, "p entry"); err != nil {
		t.Fatal(err)
	}
	got := l.MemoryPreamble(repo)
	if !strings.Contains(got, `<memory scope="user">`) {
		t.Errorf("missing user block: %s", got)
	}
	if !strings.Contains(got, `<memory scope="project">`) {
		t.Errorf("missing project block: %s", got)
	}
	// user first, project second.
	uIdx := strings.Index(got, "user")
	pIdx := strings.Index(got, "project")
	if uIdx < 0 || pIdx < 0 || uIdx > pIdx {
		t.Errorf("order wrong: %s", got)
	}
}

func TestMemoryPreamble_EmptyWhenNoFiles(t *testing.T) {
	l := NewLoader(t.TempDir())
	if got := l.MemoryPreamble(t.TempDir()); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestMemoryPreamble_SkipsProjectWhenNoRepo(t *testing.T) {
	l := NewLoader(t.TempDir())
	if _, err := l.AppendMemory("", ScopeUser, "u entry"); err != nil {
		t.Fatal(err)
	}
	got := l.MemoryPreamble("")
	if !strings.Contains(got, "user") {
		t.Errorf("want user block, got %q", got)
	}
	if strings.Contains(got, "project") {
		t.Errorf("project should be absent without repo: %q", got)
	}
}
