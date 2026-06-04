// Package catalog fetches and caches the list of available Claude models from
// the Anthropic REST API, with an in-memory TTL cache and a curated fallback.
// The API key it holds is read-only (listing only) and is intentionally kept
// out of the spawned CLI subprocess environment.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Model is one selectable model: its API id and a human label.
type Model struct {
	ID    string
	Label string
}

// Provider serves the model list, preferring the live API and degrading to the
// last-good or curated fallback list.
type Provider struct {
	apiKey   string
	client   *http.Client
	fallback []Model
	ttl      time.Duration
	now      func() time.Time
	logger   *slog.Logger
	baseURL  string // overridable in tests; default api.anthropic.com

	mu        sync.Mutex
	cached    []Model
	fetchedAt time.Time
}

// New builds a Provider. apiKey may be empty (feature off → fallback only).
func New(apiKey string, client *http.Client, fallback []Model, ttl time.Duration, logger *slog.Logger) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		apiKey:   apiKey,
		client:   client,
		fallback: fallback,
		ttl:      ttl,
		now:      time.Now,
		logger:   logger,
		baseURL:  "https://api.anthropic.com",
	}
}

// Models returns the current model list. Never returns an empty slice as long
// as a non-empty fallback was provided. With no key it returns the fallback and
// makes no network call.
//
// The lock is held across the fetch so concurrent callers don't issue duplicate
// requests; the bot's request concurrency is ~1 (single allowed user), so this
// is intentionally simple rather than using a background refresher.
func (p *Provider) Models(ctx context.Context) []Model {
	if p.apiKey == "" {
		return p.fallback
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.cached) > 0 && p.now().Sub(p.fetchedAt) < p.ttl {
		return p.cached
	}
	models, err := p.fetch(ctx)
	if err != nil {
		p.logger.Warn("model catalog fetch failed; using fallback", "err", err)
		if len(p.cached) > 0 {
			return p.cached
		}
		return p.fallback
	}
	p.cached = models
	p.fetchedAt = p.now()
	return models
}

// Contains reports whether id is present in models.
func Contains(models []Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

type apiModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type apiResp struct {
	Data    []apiModel `json:"data"`
	HasMore bool       `json:"has_more"`
	LastID  string     `json:"last_id"`
}

func (p *Provider) fetch(ctx context.Context) ([]Model, error) {
	var out []Model
	afterID := ""
	for {
		page, hasMore, lastID, err := p.fetchPage(ctx, afterID)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if !hasMore || lastID == "" {
			break
		}
		afterID = lastID
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalog: empty model list")
	}
	return out, nil
}

func (p *Provider) fetchPage(ctx context.Context, afterID string) ([]Model, bool, string, error) {
	u := p.baseURL + "/v1/models?limit=100"
	if afterID != "" {
		u += "&after_id=" + url.QueryEscape(afterID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, "", err
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, false, "", fmt.Errorf("catalog: GET /v1/models: %s: %s", resp.Status, string(body))
	}

	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, false, "", fmt.Errorf("catalog: decode: %w", err)
	}

	models := make([]Model, 0, len(r.Data))
	for _, m := range r.Data {
		label := m.DisplayName
		if label == "" {
			label = m.ID
		}
		models = append(models, Model{ID: m.ID, Label: label})
	}
	return models, r.HasMore, r.LastID, nil
}
