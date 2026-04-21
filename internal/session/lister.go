package session

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Lister enumerates Claude Code session JSONL files beneath a projects
// directory. Constructed once with the Claude home dir (e.g. /data/.claude).
type Lister struct {
	projectsDir string
}

// NewLister returns a Lister rooted at <claudeHome>/projects.
func NewLister(claudeHome string) *Lister {
	return &Lister{projectsDir: filepath.Join(claudeHome, "projects")}
}

// List returns up to limit sessions (most-recent first) parsed from the
// projects directory. A missing directory yields an empty slice, not an
// error — a first-run bot has no sessions yet.
func (l *Lister) List(limit int) ([]SessionMeta, error) {
	paths, err := l.walk()
	if err != nil {
		return nil, err
	}

	type stamped struct {
		path    string
		modTime int64
	}
	stats := make([]stamped, 0, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		stats = append(stats, stamped{path: p, modTime: info.ModTime().UnixNano()})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].modTime > stats[j].modTime })

	if limit > 0 && len(stats) > limit {
		stats = stats[:limit]
	}

	out := make([]SessionMeta, 0, len(stats))
	for _, s := range stats {
		meta, err := Parse(s.path)
		if err != nil {
			// A partly-written file isn't fatal; skip it.
			continue
		}
		out = append(out, meta)
	}
	return out, nil
}

// Find locates a session by its UUID. Returns os.ErrNotExist if absent.
func (l *Lister) Find(sessionID string) (SessionMeta, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\") {
		return SessionMeta{}, fmt.Errorf("session: invalid session id")
	}
	name := sessionID + ".jsonl"
	var found string
	err := filepath.WalkDir(l.projectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == name {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return SessionMeta{}, fmt.Errorf("session: walk: %w", err)
	}
	if found == "" {
		return SessionMeta{}, os.ErrNotExist
	}
	return Parse(found)
}

func (l *Lister) walk() ([]string, error) {
	info, err := os.Stat(l.projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: stat projects dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session: %s is not a directory", l.projectsDir)
	}

	var paths []string
	err = filepath.WalkDir(l.projectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("session: walk: %w", err)
	}
	return paths, nil
}
