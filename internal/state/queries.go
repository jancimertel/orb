package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Store wraps a SQLite connection and exposes typed operations.
// Raw SQL should live only in this file.
type Store struct {
	db *sql.DB
}

// ErrNotFound is returned when a queried row is missing.
var ErrNotFound = errors.New("state: not found")

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// --- chat_state -----------------------------------------------------------

type ChatState struct {
	ChatID           int64
	ActiveRepoAlias  string
	ActiveSessionID  string
	ActiveModel      string
	ActiveAgent      string
	UpdatedAt        time.Time
}

func (s *Store) GetChatState(ctx context.Context, chatID int64) (*ChatState, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT chat_id, COALESCE(active_repo_alias, ''), COALESCE(active_session_id, ''),
		       COALESCE(active_model, ''), COALESCE(active_agent, ''), COALESCE(updated_at, '')
		FROM chat_state WHERE chat_id = ?`, chatID)
	var (
		cs        ChatState
		updatedAt string
	)
	if err := row.Scan(&cs.ChatID, &cs.ActiveRepoAlias, &cs.ActiveSessionID, &cs.ActiveModel, &cs.ActiveAgent, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("chat_state select: %w", err)
	}
	if updatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			cs.UpdatedAt = t
		}
	}
	return &cs, nil
}

func (s *Store) SetActiveRepo(ctx context.Context, chatID int64, alias string) error {
	return s.upsertChatState(ctx, chatID, "active_repo_alias", alias)
}

func (s *Store) SetActiveSession(ctx context.Context, chatID int64, sessionID string) error {
	return s.upsertChatState(ctx, chatID, "active_session_id", sessionID)
}

func (s *Store) SetActiveModel(ctx context.Context, chatID int64, model string) error {
	return s.upsertChatState(ctx, chatID, "active_model", model)
}

func (s *Store) SetActiveAgent(ctx context.Context, chatID int64, name string) error {
	return s.upsertChatState(ctx, chatID, "active_agent", name)
}

// upsertChatState writes a single mutable column on chat_state, inserting the
// row if missing. The column name is a compile-time constant from the callers
// above, never user input, so interpolation here is safe.
func (s *Store) upsertChatState(ctx context.Context, chatID int64, column, value string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	q := fmt.Sprintf(`
		INSERT INTO chat_state (chat_id, %[1]s, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET %[1]s = excluded.%[1]s, updated_at = excluded.updated_at`, column)
	if _, err := s.db.ExecContext(ctx, q, chatID, value, now); err != nil {
		return fmt.Errorf("chat_state upsert %s: %w", column, err)
	}
	return nil
}

// --- repos ----------------------------------------------------------------

type Repo struct {
	ChatID        int64
	Alias         string
	URL           string
	Path          string
	DefaultBranch string
	AddedAt       time.Time
}

func (s *Store) AddRepo(ctx context.Context, r Repo) error {
	if r.AddedAt.IsZero() {
		r.AddedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO repos (chat_id, alias, url, path, default_branch, added_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ChatID, r.Alias, r.URL, r.Path, r.DefaultBranch, r.AddedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("repos insert: %w", err)
	}
	return nil
}

func (s *Store) RemoveRepo(ctx context.Context, chatID int64, alias string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM repos WHERE chat_id = ? AND alias = ?`, chatID, alias)
	if err != nil {
		return fmt.Errorf("repos delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetRepo(ctx context.Context, chatID int64, alias string) (*Repo, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT chat_id, alias, url, path, COALESCE(default_branch, ''), COALESCE(added_at, '')
		FROM repos WHERE chat_id = ? AND alias = ?`, chatID, alias)
	var (
		r       Repo
		addedAt string
	)
	if err := row.Scan(&r.ChatID, &r.Alias, &r.URL, &r.Path, &r.DefaultBranch, &addedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repos select: %w", err)
	}
	if addedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, addedAt); err == nil {
			r.AddedAt = t
		}
	}
	return &r, nil
}

func (s *Store) ListRepos(ctx context.Context, chatID int64) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT chat_id, alias, url, path, COALESCE(default_branch, ''), COALESCE(added_at, '')
		FROM repos WHERE chat_id = ? ORDER BY alias`, chatID)
	if err != nil {
		return nil, fmt.Errorf("repos list: %w", err)
	}
	defer rows.Close()

	var out []Repo
	for rows.Next() {
		var (
			r       Repo
			addedAt string
		)
		if err := rows.Scan(&r.ChatID, &r.Alias, &r.URL, &r.Path, &r.DefaultBranch, &addedAt); err != nil {
			return nil, fmt.Errorf("repos scan: %w", err)
		}
		if addedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, addedAt); err == nil {
				r.AddedAt = t
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- usage ----------------------------------------------------------------

type UsageDelta struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
}

type UsageRow struct {
	ChatID           int64
	Day              string // YYYY-MM-DD
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
}

// AddUsage accumulates a delta onto the row for (chatID, today-UTC).
func (s *Store) AddUsage(ctx context.Context, chatID int64, d UsageDelta) error {
	day := time.Now().UTC().Format("2006-01-02")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage (chat_id, day, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, day) DO UPDATE SET
		  input_tokens       = input_tokens       + excluded.input_tokens,
		  output_tokens      = output_tokens      + excluded.output_tokens,
		  cache_read_tokens  = cache_read_tokens  + excluded.cache_read_tokens,
		  cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
		  cost_usd           = cost_usd           + excluded.cost_usd`,
		chatID, day, d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.CacheWriteTokens, d.CostUSD)
	if err != nil {
		return fmt.Errorf("usage upsert: %w", err)
	}
	return nil
}

func (s *Store) UsageToday(ctx context.Context, chatID int64) (UsageRow, error) {
	day := time.Now().UTC().Format("2006-01-02")
	return s.usageForDay(ctx, chatID, day)
}

func (s *Store) UsageMonthToDate(ctx context.Context, chatID int64) (UsageRow, error) {
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	row := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		       COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
		       COALESCE(SUM(cost_usd),0)
		FROM usage WHERE chat_id = ? AND day >= ?`, chatID, monthStart)
	u := UsageRow{ChatID: chatID, Day: monthStart}
	if err := row.Scan(&u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &u.CostUSD); err != nil {
		return UsageRow{}, fmt.Errorf("usage mtd: %w", err)
	}
	return u, nil
}

func (s *Store) usageForDay(ctx context.Context, chatID int64, day string) (UsageRow, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT chat_id, day, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd
		FROM usage WHERE chat_id = ? AND day = ?`, chatID, day)
	var u UsageRow
	if err := row.Scan(&u.ChatID, &u.Day, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &u.CostUSD); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UsageRow{ChatID: chatID, Day: day}, nil
		}
		return UsageRow{}, fmt.Errorf("usage day: %w", err)
	}
	return u, nil
}
