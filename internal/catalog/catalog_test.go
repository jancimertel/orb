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
	if got[1] != (Model{ID: "claude-x", Label: "claude-x"}) {
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
	p.Models(context.Background())
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected 1 call within TTL, got %d", calls)
	}

	now = now.Add(2 * time.Hour)
	p.Models(context.Background())
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 calls after TTL, got %d", calls)
	}
}

func TestContains(t *testing.T) {
	models := []Model{{ID: "a"}, {ID: "b"}}
	if !Contains(models, "a") {
		t.Error("expected a to be present")
	}
	if Contains(models, "z") {
		t.Error("did not expect z")
	}
}
