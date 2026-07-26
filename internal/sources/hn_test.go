package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHNFetch_MappingAndDedup(t *testing.T) {
	// Two keyword queries; the second repeats objectID "1" to exercise dedupe,
	// and "2" has an empty url to exercise the item-link fallback.
	responses := map[string]string{
		"datadog": `{"hits":[
			{"objectID":"1","title":"Datadog bill","url":"https://ex.com/a","author":"alice","points":42,"num_comments":7,"created_at_i":1700000000},
			{"objectID":"2","title":"No URL story","url":"","author":"bob","points":3,"num_comments":1,"created_at_i":1700000100}
		]}`,
		"apm": `{"hits":[
			{"objectID":"1","title":"Datadog bill","url":"https://ex.com/a","author":"alice","points":42,"num_comments":7,"created_at_i":1700000000}
		]}`,
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		q := r.URL.Query().Get("query")
		body, ok := responses[q]
		if !ok {
			t.Errorf("unexpected query %q", q)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if tags := r.URL.Query().Get("tags"); tags != "story" {
			t.Errorf("tags = %q, want story", tags)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	h := NewHN([]string{"datadog", "apm", ""}) // empty keyword must be skipped
	h.endpoint = srv.URL

	got, err := h.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("server hit %d times, want 2 (empty keyword skipped)", got)
	}
	if len(got) != 2 {
		t.Fatalf("got %d signals, want 2 (objectID 1 deduped across queries)", len(got))
	}

	first := got[0]
	if first.Source != "hn" || first.ID != "1" {
		t.Errorf("first signal source/id = %q/%q, want hn/1", first.Source, first.ID)
	}
	if first.URL != "https://ex.com/a" {
		t.Errorf("first URL = %q, want the story url", first.URL)
	}
	if first.Permalink != "https://news.ycombinator.com/item?id=1" {
		t.Errorf("first Permalink = %q, want the discussion thread", first.Permalink)
	}
	if first.Score != 42 || first.NumComments != 7 {
		t.Errorf("score/comments = %d/%d, want 42/7", first.Score, first.NumComments)
	}
	if first.CreatedAt.Unix() != 1700000000 {
		t.Errorf("CreatedAt = %v, want unix 1700000000", first.CreatedAt)
	}

	second := got[1]
	// Ask HN (no external url): URL stays empty, Permalink is the discussion thread.
	if second.URL != "" {
		t.Errorf("empty-url signal URL = %q, want empty (no external link)", second.URL)
	}
	wantThread := "https://news.ycombinator.com/item?id=2"
	if second.Permalink != wantThread {
		t.Errorf("empty-url signal Permalink = %q, want %q", second.Permalink, wantThread)
	}
}

func TestHNFetch_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	h := NewHN([]string{"x"})
	h.endpoint = srv.URL

	if _, err := h.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}
