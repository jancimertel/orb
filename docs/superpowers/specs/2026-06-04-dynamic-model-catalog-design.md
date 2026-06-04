# Dynamic model catalog via read-only key

**Date:** 2026-06-04
**Status:** Approved, pending implementation

## Goal

Let the `/model` picker reflect Anthropic's currently-available models without a
restart or code change, so a newly-exposed model can be selected the moment the
API lists it. When no listing credential is configured, fall back to a curated
hard-coded list (which we keep current by hand).

## Background

Today `availableModels` ([handlers_runtime.go](../../../internal/telegram/handlers_runtime.go))
is a hard-coded slice, and `/model` rejects anything not in it (`knownModel`).
Adding a model means editing code and restarting.

We evaluated reusing the subscription OAuth token (`$HOME/.claude/.credentials.json`)
to call the REST API and **rejected it**: as of Feb 2026 Anthropic restricts
subscription OAuth tokens to Claude Code / claude.ai and actively blocks
third-party reuse (ToS violation), and `/v1/models` is documented to require an
`x-api-key`, not a Bearer token. The sanctioned route is a real console API key.

`/v1/models` listing is free, so a dedicated **read-only** key adds no run cost.
The bot stays on its Pro/Max subscription for actual runs by keeping that key
out of the subprocess environment entirely.

## Design

### Config — `internal/config`

Add one optional env var:

```go
ModelsAPIKey string `env:"MODELS_API_KEY"` // read-only console key, listing only
```

- Empty ⇒ feature off: the provider serves only the curated list, makes no HTTP
  calls.
- It is **never** assigned to `RegistryConfig.APIKey` and **never** reaches
  `claude.SpawnOpts` / `buildEnv`. It is distinct from `AnthropicAPIKey` (which
  *does* go to the subprocess). This separation is the whole point — runs bill to
  the subscription; listing uses the key.

### New package — `internal/catalog`

```go
type Model struct { ID, Label string }

type Provider interface {
    Models(ctx context.Context) []Model
}
```

Concrete provider:
- Constructed with: the read-only key (may be empty), an `*http.Client` (caller
  sets a timeout, e.g. 10s), the curated fallback `[]Model`, a TTL (~1h), a
  `now func() time.Time` clock (for testable caching), and a logger.
- `Models`:
  - Empty key ⇒ return curated fallback, no HTTP.
  - Cache fresh (age < TTL) ⇒ return cached list.
  - Cache stale/empty ⇒ fetch; on success cache + return; on error log and
    return last-good (if any) else curated fallback.
- Fetch: `GET https://api.anthropic.com/v1/models` with headers
  `x-api-key: <key>` and `anthropic-version: 2023-06-01`. Follow pagination via
  `has_more` + `last_id` (`?after_id=`). Map each `data[]` entry: `ID = id`,
  `Label = display_name` (fall back to `id` when blank).
- Concurrency-safe (mutex around the cache).

The curated fallback passed in is the current `availableModels` set, converted to
`[]catalog.Model`. `availableModels` stays the single source of the fallback.

### Router wiring — `internal/telegram`

- Add `catalog catalog.Provider` to the `Router` struct and a parameter to
  `NewRouter`; construct it in [main.go](../../../cmd/bot/main.go) from
  `cfg.ModelsAPIKey` + an `http.Client{Timeout: …}` + the curated fallback.
- `handleModel`: build the keyboard from `r.catalog.Models(ctx)` instead of the
  package-level `availableModels`.
- `knownModel` ⇒ a method that checks membership in `r.catalog.Models(ctx)`.
- Typed `/model <id>` whose id is **not** in the catalog: **accept with a
  warning** ("'<id>' isn't in the known list — trying it anyway; it'll fail on
  the next turn if the model doesn't exist") and persist/apply it normally. This
  preserves no-restart adoption of a brand-new id before the catalog refreshes.
  Button-callback ids always come from the catalog, so they're always valid.
- `applyModelChange`, `activeModelFor`, persistence, runner reset, and the
  `DEFAULT_MODEL` fallback are unchanged.

### Curated fallback content

`availableModels` is updated to the current lineup (done as part of this work):
`claude-opus-4-8` (Opus 4.8), `claude-opus-4-7` (Opus 4.7),
`claude-sonnet-4-6` (Sonnet 4.6), `claude-haiku-4-5-20251001` (Haiku 4.5).

## Tests

- `internal/catalog`:
  - httptest server returns a `data` payload ⇒ parsed to the right `Model`s
    (display_name → Label, blank → id).
  - pagination across two pages (`has_more` then not).
  - 4xx / 5xx / timeout ⇒ returns fallback (or last-good if previously cached).
  - empty key ⇒ returns fallback and makes zero HTTP calls.
  - TTL: with an injected clock, a second call within TTL doesn't re-fetch; after
    TTL it does.
- `internal/telegram`: `knownModel` true for a catalog id, false otherwise;
  typed-unknown id is accepted with a warning and persisted.

## Out of scope (YAGNI)

- Background refresh goroutine — lazy refresh on access is enough.
- Persisting the catalog to SQLite.
- Filtering deprecated/old models — revisit only if the live list gets noisy.
- Any use of the subscription OAuth token (rejected on ToS + technical grounds).

## Acceptance criteria

- With `MODELS_API_KEY` set, `/model` lists models from `/v1/models`; a model
  Anthropic newly exposes appears without a restart.
- With `MODELS_API_KEY` unset, `/model` shows the curated list and makes no
  network calls; `MODELS_API_KEY` is absent from every spawned subprocess's env.
- A failed/timed-out listing call degrades to the curated (or last-good) list,
  never an error to the user.
- Typed `/model <unknown-id>` is accepted with a warning; button selections are
  always valid.
- New + existing tests pass.
