package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLobstersFetch_Mapping(t *testing.T) {
	// Story A: submitter_user as a bare string, has a url.
	// Story B: submitter_user as an object, empty url (falls back to comments_url).
	body := `[
		{"short_id":"aaa","created_at":"2023-06-01T12:00:00.000-04:00","title":"A","url":"https://ex.com/a","score":10,"comment_count":4,"description_plain":"desc a","comments_url":"https://lobste.rs/s/aaa","submitter_user":"alice"},
		{"short_id":"bbb","created_at":"2023-06-01T13:00:00.000-04:00","title":"B","url":"","score":2,"comment_count":0,"description_plain":"desc b","comments_url":"https://lobste.rs/s/bbb","submitter_user":{"username":"bob"}}
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	l := NewLobsters()
	l.endpoint = srv.URL

	got, err := l.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d signals, want 2", len(got))
	}

	a := got[0]
	if a.Source != "lobsters" || a.ID != "aaa" || a.Author != "alice" {
		t.Errorf("A source/id/author = %q/%q/%q", a.Source, a.ID, a.Author)
	}
	if a.URL != "https://ex.com/a" {
		t.Errorf("A url = %q, want story url", a.URL)
	}
	if a.CreatedAt.IsZero() {
		t.Error("A CreatedAt should parse from RFC3339 with millis + offset")
	}

	b := got[1]
	if b.Author != "bob" {
		t.Errorf("B author = %q, want bob (object form of submitter_user)", b.Author)
	}
	// Text post (no external url): URL stays empty, Permalink is the comments thread.
	if b.URL != "" {
		t.Errorf("B URL = %q, want empty (no external link)", b.URL)
	}
	if b.Permalink != "https://lobste.rs/s/bbb" {
		t.Errorf("B Permalink = %q, want comments_url", b.Permalink)
	}
}

func TestLobstersFetch_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	l := NewLobsters()
	l.endpoint = srv.URL

	if _, err := l.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}
