package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestLobstersFetch_Mapping(t *testing.T) {
	// Story A: submitter_user as a bare string, has a url and tags.
	// Story B: submitter_user as an object, empty url (falls back to comments_url).
	body := `[
		{"short_id":"aaa","created_at":"2023-06-01T12:00:00.000-04:00","title":"A","url":"https://ex.com/a","score":10,"comment_count":4,"description_plain":"desc a","comments_url":"https://lobste.rs/s/aaa","tags":["devops","performance"],"submitter_user":"alice"},
		{"short_id":"bbb","created_at":"2023-06-01T13:00:00.000-04:00","title":"B","url":"","score":2,"comment_count":0,"description_plain":"desc b","comments_url":"https://lobste.rs/s/bbb","submitter_user":{"username":"bob"}}
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	l := NewLobsters(nil)
	l.base = srv.URL

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
	// Tags are the topicality carrier for link posts, so they must survive
	// normalization into the Signal or the pre-filter has nothing to match.
	if strings.Join(a.Tags, ",") != "devops,performance" {
		t.Errorf("A tags = %v, want [devops performance]", a.Tags)
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
	if len(b.Tags) != 0 {
		t.Errorf("B tags = %v, want none", b.Tags)
	}
}

func TestLobstersFetch_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	l := NewLobsters(nil)
	l.base = srv.URL

	if _, err := l.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}

// TestLobstersFetch_TagFeed covers the whole point of the tag support: a
// configured beacon polls the global newest feed AND the tag-scoped feed, and a
// story appearing in both is emitted once.
func TestLobstersFetch_TagFeed(t *testing.T) {
	var mu sync.Mutex
	var paths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/newest.json":
			// "shared" also appears in the tag feed; "global" only here.
			_, _ = w.Write([]byte(`[
				{"short_id":"shared","title":"Shared","comments_url":"https://lobste.rs/s/shared","tags":["devops"]},
				{"short_id":"global","title":"Global","comments_url":"https://lobste.rs/s/global","tags":["ml"]}
			]`))
		case "/t/devops,performance.json":
			_, _ = w.Write([]byte(`[
				{"short_id":"shared","title":"Shared","comments_url":"https://lobste.rs/s/shared","tags":["devops"]},
				{"short_id":"tagged","title":"Tagged","comments_url":"https://lobste.rs/s/tagged","tags":["performance"]}
			]`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Duplicates, blanks, mixed case and surrounding space must normalize away
	// rather than produce a malformed tag path.
	l := NewLobsters([]string{"DevOps", " performance ", "devops", ""})
	l.base = srv.URL

	got, err := l.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mu.Lock()
	gotPaths := strings.Join(paths, " ")
	mu.Unlock()
	if !strings.Contains(gotPaths, "/newest.json") || !strings.Contains(gotPaths, "/t/devops,performance.json") {
		t.Errorf("polled paths = %q, want both the newest feed and the tag feed", gotPaths)
	}

	ids := make([]string, 0, len(got))
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if len(got) != 3 {
		t.Fatalf("got %d signals %v, want 3 deduped (shared once)", len(got), ids)
	}
	seen := map[string]int{}
	prenarrowed := map[string]bool{}
	for _, s := range got {
		seen[s.ID]++
		prenarrowed[s.ID] = s.Prenarrowed
	}
	if seen["shared"] != 1 {
		t.Errorf("story in both feeds emitted %d times, want 1 (ids: %v)", seen["shared"], ids)
	}
	// Tag-feed results are pre-narrowed; global-feed-only results stay gated,
	// since that feed is an all-topics firehose.
	if !prenarrowed["tagged"] {
		t.Error("a tag-feed story must be marked Prenarrowed so the keyword gate is skipped")
	}
	if prenarrowed["global"] {
		t.Error("a global-feed story must NOT be marked Prenarrowed")
	}
	for _, want := range []string{"global", "tagged"} {
		if seen[want] != 1 {
			t.Errorf("missing story %q from the merged result (ids: %v)", want, ids)
		}
	}
}

// TestLobstersFetch_OneFeedFailing asserts a failing feed does not sink the
// other: the error is reported, but the surviving feed's signals still come back.
func TestLobstersFetch_OneFeedFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/newest.json" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`[{"short_id":"tagged","title":"Tagged","comments_url":"https://lobste.rs/s/tagged"}]`))
	}))
	defer srv.Close()

	l := NewLobsters([]string{"devops"})
	l.base = srv.URL

	got, err := l.Fetch(context.Background())
	if err == nil {
		t.Error("expected the failing feed to be reported as an error")
	}
	if len(got) != 1 || got[0].ID != "tagged" {
		t.Errorf("got %v, want the surviving feed's single signal", got)
	}
}

func TestNormalizeTags_Caps(t *testing.T) {
	in := make([]string, lobstersMaxTags+5)
	for i := range in {
		in[i] = string(rune('a' + i))
	}
	if got := normalizeTags(in); len(got) != lobstersMaxTags {
		t.Errorf("normalizeTags kept %d tags, want the %d cap", len(got), lobstersMaxTags)
	}
}
