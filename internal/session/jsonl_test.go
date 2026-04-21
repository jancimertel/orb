package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a small helper for fixture files.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParse_LastExchangeSummary(t *testing.T) {
	// Two turns: user then assistant. Parser should build a Q/A summary,
	// capture cwd from the first user line, count messages, and pick
	// start/last timestamps correctly.
	const jsonl = `{"type":"user","cwd":"/repo","timestamp":"2026-04-20T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"make it blue"}]}}
{"type":"assistant","cwd":"/repo","timestamp":"2026-04-20T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"done — changed color to blue"}]}}
`
	dir := t.TempDir()
	p := writeFile(t, dir, "abc-123.jsonl", jsonl)

	meta, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != "abc-123" {
		t.Errorf("ID = %q, want abc-123", meta.ID)
	}
	if meta.CWD != "/repo" {
		t.Errorf("CWD = %q", meta.CWD)
	}
	if meta.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", meta.MessageCount)
	}
	if !strings.Contains(meta.Summary, "Q: make it blue") {
		t.Errorf("Summary missing user text: %q", meta.Summary)
	}
	if !strings.Contains(meta.Summary, "A: done") {
		t.Errorf("Summary missing assistant text: %q", meta.Summary)
	}
	if meta.StartedAt.IsZero() || meta.LastActivityAt.Before(meta.StartedAt) {
		t.Errorf("bad timestamps: start=%v last=%v", meta.StartedAt, meta.LastActivityAt)
	}
}

func TestParse_PrefersSummaryLine(t *testing.T) {
	// When a summary-typed line exists, it wins over the last-exchange fallback.
	const jsonl = `{"type":"summary","summary":"Refactor auth middleware","leafUuid":"abc"}
{"type":"user","cwd":"/repo","timestamp":"2026-04-20T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"assistant","cwd":"/repo","timestamp":"2026-04-20T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
`
	p := writeFile(t, t.TempDir(), "s.jsonl", jsonl)
	meta, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Summary != "Refactor auth middleware" {
		t.Errorf("Summary = %q, want the explicit summary line", meta.Summary)
	}
}

func TestParse_SkipsNonMessageTypes(t *testing.T) {
	// queue-operation / telemetry lines are common in Claude Code logs and
	// must not inflate MessageCount or leak into the summary.
	const jsonl = `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-04-20T10:00:00Z"}
{"type":"user","cwd":"/repo","timestamp":"2026-04-20T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"one"}]}}
{"type":"queue-operation","operation":"dequeue","timestamp":"2026-04-20T10:00:02Z"}
{"type":"assistant","cwd":"/repo","timestamp":"2026-04-20T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"two"}]}}
`
	p := writeFile(t, t.TempDir(), "s.jsonl", jsonl)
	meta, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", meta.MessageCount)
	}
}

func TestParse_ContentAsString(t *testing.T) {
	// Some CLI versions emit message.content as a plain string rather than
	// an array of blocks. Parser should handle both.
	const jsonl = `{"type":"user","cwd":"/repo","timestamp":"2026-04-20T10:00:00Z","message":{"role":"user","content":"plain text body"}}
`
	p := writeFile(t, t.TempDir(), "s.jsonl", jsonl)
	meta, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta.Summary, "plain text body") {
		t.Errorf("Summary missing string content: %q", meta.Summary)
	}
}

func TestParse_MalformedLinesSkipped(t *testing.T) {
	// A broken line should not abort parsing of the rest of the file.
	const jsonl = `{"type":"user","cwd":"/repo","timestamp":"2026-04-20T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"ok"}]}}
not-json-at-all
{"type":"assistant","cwd":"/repo","timestamp":"2026-04-20T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"also ok"}]}}
`
	p := writeFile(t, t.TempDir(), "s.jsonl", jsonl)
	meta, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2 despite bad line", meta.MessageCount)
	}
}

func TestLister_ListAndFind(t *testing.T) {
	claudeHome := t.TempDir()
	projects := filepath.Join(claudeHome, "projects", "proj-a")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	a := writeFile(t, projects, "a.jsonl", `{"type":"user","cwd":"/x","timestamp":"2026-04-20T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"A"}]}}
`)
	b := writeFile(t, projects, "b.jsonl", `{"type":"user","cwd":"/x","timestamp":"2026-04-20T11:00:00Z","message":{"role":"user","content":[{"type":"text","text":"B"}]}}
`)
	_ = a
	// Bump mtime of b so it sorts first.
	now := mustStat(t, b).ModTime().Add(60)
	_ = os.Chtimes(b, now, now)

	l := NewLister(claudeHome)
	metas, err := l.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d, want 2", len(metas))
	}
	if metas[0].ID != "b" {
		t.Errorf("first = %q, want b (most recent)", metas[0].ID)
	}

	found, err := l.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != "a" {
		t.Errorf("Find returned %q", found.ID)
	}

	if _, err := l.Find("nope"); err == nil {
		t.Error("expected error for missing session")
	}
}

func TestLister_MissingProjectsDir(t *testing.T) {
	// Fresh install has no ~/.claude yet. Must be non-fatal.
	l := NewLister(t.TempDir())
	metas, err := l.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 0 {
		t.Errorf("expected empty, got %d", len(metas))
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
