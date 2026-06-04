# Dynamic Model Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/model` list Anthropic's currently-available models live (via `GET /v1/models` using a read-only `MODELS_API_KEY`), falling back to a curated hard-coded list when the key is unset or the call fails — so a newly-exposed model is selectable without a restart.

**Architecture:** A new `internal/catalog` package fetches and caches the model list with an in-memory TTL and a curated fallback; the read-only key is held only by this provider and never reaches the spawned subprocess. The Telegram `/model` handler builds its keyboard from the provider; typed unknown ids are accepted with a warning.

**Tech Stack:** Go (`net/http`, `encoding/json`), telego (Telegram), Claude Code CLI subprocess.

**Spec:** `docs/superpowers/specs/2026-06-04-dynamic-model-catalog-design.md`

---

### Task 1: The `internal/catalog` provider

**Files:**
- Create: `internal/catalog/catalog.go`
- Create: `internal/catalog/catalog_test.go`

- [ ] **Step 1: Write the provider**

Create `internal/catalog/catalog.go`:

```go
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
```

- [ ] **Step 2: Write the failing tests**

Create `internal/catalog/catalog_test.go`:

```go
package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

var fallback = []Model{{ID: "claude-fallback", Label: "Fallback"}}

func newWith(t *testing.T, key string, handler http.HandlerFunc) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := New(key, srv.Client(), fallback, time.Hour, nil)
	p.baseURL = srv.URL
	return p, srv
}

func TestModels_ParsesLiveList(t *testing.T) {
	p, _ := newWith(t, "k", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "k" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		fmt.Fprint(w, `{"data":[{"id":"claude-opus-4-8","display_name":"Opus 4.8"},{"id":"claude-x"}],"has_more":false,"last_id":"claude-x"}`)
	})
	got := p.Models(context.Background())
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %v", len(got), got)
	}
	if got[0] != (Model{ID: "claude-opus-4-8", Label: "Opus 4.8"}) {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1] != (Model{ID: "claude-x", Label: "claude-x"}) { // blank display_name → id
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestModels_Paginates(t *testing.T) {
	p, _ := newWith(t, "k", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after_id") == "" {
			fmt.Fprint(w, `{"data":[{"id":"a","display_name":"A"}],"has_more":true,"last_id":"a"}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"b","display_name":"B"}],"has_more":false,"last_id":"b"}`)
	})
	got := p.Models(context.Background())
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("pagination failed: %v", got)
	}
}

func TestModels_FallbackOnError(t *testing.T) {
	p, _ := newWith(t, "k", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	got := p.Models(context.Background())
	if len(got) != 1 || got[0].ID != "claude-fallback" {
		t.Fatalf("expected fallback, got %v", got)
	}
}

func TestModels_EmptyKeyMakesNoCall(t *testing.T) {
	var calls int32
	p, _ := newWith(t, "", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, `{"data":[{"id":"x"}],"has_more":false}`)
	})
	got := p.Models(context.Background())
	if len(got) != 1 || got[0].ID != "claude-fallback" {
		t.Fatalf("expected fallback, got %v", got)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("expected 0 HTTP calls, got %d", calls)
	}
}

func TestModels_CachesWithinTTL(t *testing.T) {
	var calls int32
	p, _ := newWith(t, "k", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, `{"data":[{"id":"a","display_name":"A"}],"has_more":false}`)
	})
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }

	p.Models(context.Background())
	p.Models(context.Background()) // within TTL → cached
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected 1 call within TTL, got %d", calls)
	}

	now = now.Add(2 * time.Hour) // past TTL → refetch
	p.Models(context.Background())
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 calls after TTL, got %d", calls)
	}
}
```

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/catalog/ -v`
Expected: PASS for all five tests.

- [ ] **Step 4: Commit**

```bash
git add internal/catalog/
git commit -m "feat(catalog): live /v1/models provider with TTL cache and fallback"
```

---

### Task 2: Config — add `MODELS_API_KEY`

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add the field**

In `internal/config/config.go`, add below `AnthropicAPIKey` (keep it grouped with the other Anthropic auth, with a clear comment):

```go
	// ModelsAPIKey is a READ-ONLY Anthropic console API key used ONLY to list
	// models via GET /v1/models. It is never injected into spawned CLI
	// subprocesses, so model runs stay on the subscription. Empty = use the
	// curated fallback list and make no network calls.
	ModelsAPIKey string `env:"MODELS_API_KEY"`
```

- [ ] **Step 2: Build**

Run: `go build ./internal/config/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat(config): add read-only MODELS_API_KEY for model listing"
```

---

### Task 3: Wire the catalog into the Router

**Files:**
- Modify: `internal/telegram/router.go` (struct field + `NewRouter` param)
- Modify: `internal/telegram/handlers_runtime.go` (fallback converter)
- Modify: `cmd/bot/main.go` (construct provider, pass to `NewRouter`)

- [ ] **Step 1: Add the `FallbackModels` converter**

In `internal/telegram/handlers_runtime.go`, add an import for the catalog package at the top:

```go
	"github.com/jancimertel/orb/internal/catalog"
```

Then, just below the `availableModels` declaration, add:

```go
// FallbackModels exposes the curated list as catalog.Model values for use as the
// provider's offline fallback. availableModels stays the single source of truth.
func FallbackModels() []catalog.Model {
	out := make([]catalog.Model, len(availableModels))
	for i, m := range availableModels {
		out[i] = catalog.Model{ID: m.id, Label: m.label}
	}
	return out
}
```

- [ ] **Step 2: Add the Router field and constructor param**

In `internal/telegram/router.go`, add the catalog import:

```go
	"github.com/jancimertel/orb/internal/catalog"
```

Add a field to `Router` (after `registry`):

```go
	registry    *claude.Registry
	catalog     *catalog.Provider
```

Add the parameter to `NewRouter` (after `registry *claude.Registry`):

```go
	registry *claude.Registry,
	modelCatalog *catalog.Provider,
```

And set it in the returned struct literal (after `registry:    registry,`):

```go
		registry:    registry,
		catalog:     modelCatalog,
```

- [ ] **Step 3: Update the call site in main.go**

In `cmd/bot/main.go`, ensure `net/http` and `time` and the catalog package are imported:

```go
	"net/http"
	"time"

	"github.com/jancimertel/orb/internal/catalog"
```

Immediately before the existing `router := telegram.NewRouter(...)` line, construct the provider:

```go
	modelCatalog := catalog.New(
		cfg.ModelsAPIKey,
		&http.Client{Timeout: 10 * time.Second},
		telegram.FallbackModels(),
		time.Hour,
		logger,
	)
```

Then update the `NewRouter` call to pass it (after `registry,`):

```go
	router := telegram.NewRouter(bot, store, registry, modelCatalog, usageTracker, sessionLister, repoManager, agentLoader, cfg, logger)
```

Note: the new `modelCatalog` arg goes immediately after `registry` to match the constructor signature from Step 2. Double-check the argument order matches `NewRouter`'s parameter order exactly.

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: no output (success). If the compiler reports `time` or `net/http` already imported, remove the duplicate.

- [ ] **Step 5: Commit**

```bash
git add internal/telegram/router.go internal/telegram/handlers_runtime.go cmd/bot/main.go
git commit -m "feat(telegram): wire model catalog provider into Router"
```

---

### Task 4: Drive `/model` from the catalog (live list + accept-with-warning)

**Files:**
- Modify: `internal/telegram/handlers_runtime.go`

- [ ] **Step 1: Add the `context` import**

At the top of `internal/telegram/handlers_runtime.go`, add:

```go
	"context"
```

- [ ] **Step 2: Build the keyboard from the live catalog**

In `handleModel`, replace the `for _, m := range availableModels {` loop with one over the catalog. The loop body changes `m.label`/`m.id` to `m.Label`/`m.ID`:

```go
	active := r.activeModelFor(ctx, chatID)

	var rows [][]telego.InlineKeyboardButton
	for _, m := range r.catalog.Models(ctx) {
		label := m.Label
		if m.ID == active {
			label = "✓ " + label
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			{Text: label + "  (" + m.ID + ")", CallbackData: modelPrefix + modelActionSet + ":" + m.ID},
		})
	}
```

- [ ] **Step 3: Make `knownModel` catalog-aware and relax `applyModelChange`**

Replace the free function `knownModel` with a method, and move validation out of `applyModelChange` so typed unknown ids are allowed (with a warning from `setModel`). Replace these three functions:

```go
func (r *Router) setModel(ctx *th.Context, chatID int64, id string) error {
	if err := r.applyModelChange(ctx, chatID, id); err != nil {
		return r.reply(ctx, chatID, err.Error())
	}
	msg := "model set to " + id + "\n(takes effect on next turn)"
	if !r.knownModel(ctx, id) {
		msg = "⚠️ " + id + " isn't in the known list — trying it anyway; " +
			"it will fail on the next turn if the model doesn't exist.\n\n" + msg
	}
	return r.reply(ctx, chatID, msg)
}

// applyModelChange persists the model and tears down the runner so the next
// spawn picks up the new --model. It does NOT validate membership; callers
// decide messaging (button ids always come from the catalog; typed ids may be
// brand-new and are accepted with a warning).
func (r *Router) applyModelChange(ctx *th.Context, chatID int64, id string) error {
	if err := r.store.SetActiveModel(ctx, chatID, id); err != nil {
		r.logger.Error("set model failed", "chat_id", chatID, "err", err)
		return fmt.Errorf("persist failed")
	}
	r.registry.Reset(chatID)
	r.sessionAllow.Clear(chatID)
	return nil
}

func (r *Router) knownModel(ctx context.Context, id string) bool {
	return catalog.Contains(r.catalog.Models(ctx), id)
}
```

Note: `*th.Context` satisfies `context.Context` (the existing `r.store.*` calls already pass it where a `context.Context` is expected), so `r.knownModel(ctx, id)` and `r.catalog.Models(ctx)` both accept the handler's `ctx`.

- [ ] **Step 4: Confirm no other references to the old `knownModel` free function remain**

Run: `rg -n "knownModel|availableModels" internal/telegram`
Expected: `availableModels` referenced only in its declaration and `FallbackModels`; `knownModel` only as the new method and its call in `setModel`. The `handleModelCallback` path calls `applyModelChange` directly (button ids are catalog ids), so it needs no change — verify it still compiles.

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 6: Write a small test for `catalog.Contains` wiring intent**

This logic lives in the catalog package; add one focused test there if not already covered. Create/append to `internal/catalog/catalog_test.go`:

```go
func TestContains(t *testing.T) {
	models := []Model{{ID: "a"}, {ID: "b"}}
	if !Contains(models, "a") {
		t.Error("expected a to be present")
	}
	if Contains(models, "z") {
		t.Error("did not expect z")
	}
}
```

Run: `go test ./internal/catalog/ -run TestContains -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/telegram/handlers_runtime.go internal/catalog/catalog_test.go
git commit -m "feat(telegram): drive /model from live catalog, accept unknown ids with warning"
```

---

### Task 5: Documentation and full verification

**Files:**
- Modify: `.env.example`

- [ ] **Step 1: Document the env var**

Add to `.env.example` (under the Anthropic-related vars):

```
# Optional read-only Anthropic API key used ONLY to list models for /model.
# Kept out of the CLI subprocess, so model runs still bill to your subscription.
# Leave unset to use the built-in curated model list.
MODELS_API_KEY=
```

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 3: Vet**

Run: `go vet ./...`
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add .env.example
git commit -m "docs: document MODELS_API_KEY env var"
```

- [ ] **Step 5: Manual smoke (optional)**

- With `MODELS_API_KEY` unset: `/model` shows the curated list (Opus 4.8, 4.7, Sonnet 4.6, Haiku 4.5); no network call in logs.
- With a valid read-only key: `/model` shows the live list; restart not required to see a newly-exposed model after the ~1h TTL (or immediately on a fresh process).
- `/model some-future-id` (typed): replies with the ⚠️ warning and still sets it.

---

## Notes for the implementer

- The read-only `MODELS_API_KEY` must NEVER be assigned to `claude.RegistryConfig.APIKey` or appear in `claude.buildEnv`. Only `catalog.New` receives it. This is the security boundary that keeps runs on the subscription.
- Do not add a background refresh goroutine, SQLite persistence of the catalog, or deprecated-model filtering — all explicitly out of scope.
- `availableModels` remains the single source of the curated fallback; `FallbackModels()` converts it. Don't duplicate the list.
- This plan is independent of the effort-setting plan; either can land first.
