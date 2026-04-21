package repo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jancimertel/orb/internal/state"
)

// Manager clones, removes, and queries git repositories registered per chat.
// The GitHub PAT is held in memory only: it is injected into URLs for git
// network operations and kept out of both the database and .git/config.
type Manager struct {
	root   string // workspace root (e.g. /data/workspaces)
	pat    string
	store  *state.Store
	logger *slog.Logger
}

// Repo mirrors state.Repo with an added CurrentBranch field populated by
// live git queries.
type Repo struct {
	state.Repo
	CurrentBranch string
}

// Config captures manager dependencies at construction.
type Config struct {
	Root   string
	PAT    string
	Store  *state.Store
	Logger *slog.Logger
}

func NewManager(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Manager{
		root:   cfg.Root,
		pat:    cfg.PAT,
		store:  cfg.Store,
		logger: cfg.Logger,
	}
}

// validAlias enforces a filesystem- and URL-safe shape so the alias can be
// used as both a directory name and a callback_data segment.
var validAlias = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,39}$`)

// AddOpts holds the arguments of an /repo add invocation.
type AddOpts struct {
	ChatID int64
	URL    string // user-supplied (bare github.com/foo/bar also accepted)
	Alias  string // optional; derived from URL when empty
}

// Add clones the repository, registers it in the database, and returns the
// stored record.
func (m *Manager) Add(ctx context.Context, opts AddOpts) (state.Repo, error) {
	cleanURL, err := normalizeURL(opts.URL)
	if err != nil {
		return state.Repo{}, err
	}

	alias := opts.Alias
	if alias == "" {
		alias = aliasFromURL(cleanURL)
	}
	if !validAlias.MatchString(alias) {
		return state.Repo{}, fmt.Errorf("invalid alias %q (must match %s)", alias, validAlias.String())
	}

	// Reject duplicate alias up front to avoid a wasted clone.
	if existing, err := m.store.GetRepo(ctx, opts.ChatID, alias); err == nil && existing != nil {
		return state.Repo{}, fmt.Errorf("alias %q already registered", alias)
	} else if err != nil && !errors.Is(err, state.ErrNotFound) {
		return state.Repo{}, err
	}

	path := filepath.Join(m.root, fmt.Sprintf("c%d", opts.ChatID), alias)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return state.Repo{}, fmt.Errorf("mkdir workspace: %w", err)
	}

	// A leftover directory from a previous failed run would cause `git
	// clone` to abort. Refuse rather than silently clobbering something
	// that might hold the operator's work.
	if _, err := os.Stat(path); err == nil {
		return state.Repo{}, fmt.Errorf("workspace path already exists: %s", path)
	}

	authURL, err := authedURL(cleanURL, m.pat)
	if err != nil {
		return state.Repo{}, err
	}

	m.logger.Info("cloning repo",
		"chat_id", opts.ChatID,
		"alias", alias,
		"url", cleanURL,
		"path", path,
	)
	if err := clone(ctx, authURL, path); err != nil {
		_ = os.RemoveAll(path)
		return state.Repo{}, scrubPAT(err, m.pat)
	}
	// Replace the authed remote with the clean URL so the PAT isn't stored
	// on disk. Network ops will re-inject it via authedURL() at call time.
	if err := setRemoteURL(ctx, path, cleanURL); err != nil {
		_ = os.RemoveAll(path)
		return state.Repo{}, scrubPAT(err, m.pat)
	}

	branch, _ := defaultBranch(ctx, path)
	if branch == "" {
		branch, _ = currentBranch(ctx, path)
	}

	rec := state.Repo{
		ChatID:        opts.ChatID,
		Alias:         alias,
		URL:           cleanURL,
		Path:          path,
		DefaultBranch: branch,
	}
	if err := m.store.AddRepo(ctx, rec); err != nil {
		_ = os.RemoveAll(path)
		return state.Repo{}, fmt.Errorf("store add: %w", err)
	}
	return rec, nil
}

// Remove unregisters the repo for the chat and deletes its workspace directory.
// Missing entries or directories are tolerated so retries of a failed remove
// can make progress.
func (m *Manager) Remove(ctx context.Context, chatID int64, alias string) error {
	rec, err := m.store.GetRepo(ctx, chatID, alias)
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	if rec != nil {
		if err := m.removePath(rec.Path); err != nil {
			return err
		}
	}
	if err := m.store.RemoveRepo(ctx, chatID, alias); err != nil && !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return nil
}

// removePath guards against a misconfigured root by refusing to rm anything
// that isn't underneath the configured workspace root.
func (m *Manager) removePath(path string) error {
	if m.root == "" {
		return fmt.Errorf("workspace root not configured")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rootAbs, err := filepath.Abs(m.root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return fmt.Errorf("refusing to remove path outside workspace root: %s", path)
	}
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("rm -rf %s: %w", abs, err)
	}
	return nil
}

// List returns all repos registered for the chat, with the current branch
// filled in by a live git query.
func (m *Manager) List(ctx context.Context, chatID int64) ([]Repo, error) {
	rows, err := m.store.ListRepos(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(rows))
	for _, r := range rows {
		branch, _ := currentBranch(ctx, r.Path)
		out = append(out, Repo{Repo: r, CurrentBranch: branch})
	}
	return out, nil
}

// Get resolves a single repo by alias, enriched with its current branch.
func (m *Manager) Get(ctx context.Context, chatID int64, alias string) (Repo, error) {
	r, err := m.store.GetRepo(ctx, chatID, alias)
	if err != nil {
		return Repo{}, err
	}
	branch, _ := currentBranch(ctx, r.Path)
	return Repo{Repo: *r, CurrentBranch: branch}, nil
}

// Select marks alias as the active repo for the chat. Returns ErrNotFound
// if the alias is not registered.
func (m *Manager) Select(ctx context.Context, chatID int64, alias string) (state.Repo, error) {
	rec, err := m.store.GetRepo(ctx, chatID, alias)
	if err != nil {
		return state.Repo{}, err
	}
	if err := m.store.SetActiveRepo(ctx, chatID, alias); err != nil {
		return state.Repo{}, err
	}
	return *rec, nil
}

// scrubPAT strips the PAT from any error message so it never makes it to
// logs or Telegram replies.
func scrubPAT(err error, pat string) error {
	if err == nil || pat == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, pat) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, pat, "***"))
}
