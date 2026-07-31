package sources

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// redditStub stands in for both Reddit endpoints: the token exchange and the
// search API.
type redditStub struct {
	srv *httptest.Server

	mu          sync.Mutex
	tokenCalls  int
	searchPaths []string
	searchQ     []string
	authHeaders []string
	searchCode  int // when non-zero, the status search replies with
	listing     string
}

func newRedditStub(t *testing.T, listing string) *redditStub {
	t.Helper()
	s := &redditStub{listing: listing}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q, want the descriptive beacon agent", r.Header.Get("User-Agent"))
		}
		s.mu.Lock()
		defer s.mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/access_token") {
			s.tokenCalls++
			// Credentials must arrive as HTTP Basic, not in the body.
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("cid:csecret"))
			if got := r.Header.Get("Authorization"); got != want {
				t.Errorf("token auth header = %q, want basic app credentials", got)
			}
			_, _ = w.Write([]byte(`{"access_token":"TKN","expires_in":86400,"token_type":"bearer"}`))
			return
		}

		s.searchPaths = append(s.searchPaths, r.URL.Path)
		s.searchQ = append(s.searchQ, r.URL.RawQuery)
		s.authHeaders = append(s.authHeaders, r.Header.Get("Authorization"))
		if s.searchCode != 0 {
			w.WriteHeader(s.searchCode)
			return
		}
		_, _ = w.Write([]byte(s.listing))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func newTestReddit(stub *redditStub, subs, keywords []string) *Reddit {
	r := NewReddit("cid", "csecret", subs, keywords)
	r.base = stub.srv.URL
	r.tokenURL = stub.srv.URL + "/api/v1/access_token"
	return r
}

const redditListingJSON = `{"data":{"children":[
	{"data":{"name":"t3_aaa","id":"aaa","title":"Datadog bill tripled","selftext":"help","permalink":"/r/devops/comments/aaa/x/","author":"alice","subreddit":"devops","score":42,"num_comments":7,"created_utc":1700000000,"is_self":true}},
	{"data":{"name":"t3_bbb","id":"bbb","title":"A link post","url":"https://ex.com/b","permalink":"/r/sre/comments/bbb/y/","author":"bob","subreddit":"sre","score":3,"num_comments":1,"created_utc":1700000100,"is_self":false}},
	{"data":{"name":"t3_ccc","id":"ccc","title":"Weekly megathread","permalink":"/r/devops/comments/ccc/z/","author":"mod","subreddit":"devops","stickied":true}}
]}}`

func TestRedditFetch_Mapping(t *testing.T) {
	stub := newRedditStub(t, redditListingJSON)
	r := newTestReddit(stub, []string{"devops"}, []string{"datadog"})

	got, err := r.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Three posts in, one is stickied moderator furniture and must be dropped.
	if len(got) != 2 {
		t.Fatalf("got %d signals, want 2 (stickied post excluded)", len(got))
	}

	a := got[0]
	if a.Source != "reddit" || a.ID != "t3_aaa" || a.Author != "alice" {
		t.Errorf("A source/id/author = %q/%q/%q", a.Source, a.ID, a.Author)
	}
	if a.Permalink != "https://www.reddit.com/r/devops/comments/aaa/x/" {
		t.Errorf("A permalink = %q, want the absolute thread URL", a.Permalink)
	}
	// Self post: the thread is the content, so there is no external link to show.
	if a.URL != "" {
		t.Errorf("A url = %q, want empty for a self post", a.URL)
	}
	if a.Body != "help" || a.Score != 42 || a.NumComments != 7 {
		t.Errorf("A body/score/comments = %q/%d/%d", a.Body, a.Score, a.NumComments)
	}
	// The subreddit is Reddit's topic tag, and the classifier reads tags.
	if len(a.Tags) != 1 || a.Tags[0] != "r/devops" {
		t.Errorf("A tags = %v, want [r/devops]", a.Tags)
	}
	if a.CreatedAt.IsZero() {
		t.Error("A CreatedAt should parse from created_utc")
	}

	if got[1].URL != "https://ex.com/b" {
		t.Errorf("B url = %q, want the external link", got[1].URL)
	}
}

// TestRedditFetch_QueryShape pins the request contract: one multireddit call per
// keyword, restricted to the configured subreddits, newest first, bearer-authed.
func TestRedditFetch_QueryShape(t *testing.T) {
	stub := newRedditStub(t, redditListingJSON)
	// Mixed input forms and a duplicate: all must normalize into one path.
	r := newTestReddit(stub, []string{"r/devops", "/r/sre/", "DevOps"}, []string{"datadog", "mttr"})

	if _, err := r.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()

	if len(stub.searchPaths) != 2 {
		t.Fatalf("made %d search calls, want one per keyword (2)", len(stub.searchPaths))
	}
	for _, p := range stub.searchPaths {
		if p != "/r/devops+sre/search" {
			t.Errorf("search path = %q, want the deduped multireddit /r/devops+sre/search", p)
		}
	}
	for _, q := range stub.searchQ {
		for _, want := range []string{"restrict_sr=1", "sort=new", "raw_json=1"} {
			if !strings.Contains(q, want) {
				t.Errorf("query %q missing %q", q, want)
			}
		}
	}
	if strings.Join(stub.searchQ, " ") == "" || !strings.Contains(strings.Join(stub.searchQ, " "), "q=mttr") {
		t.Errorf("queries %v do not cover every keyword", stub.searchQ)
	}
	for _, h := range stub.authHeaders {
		if h != "Bearer TKN" {
			t.Errorf("search auth = %q, want the exchanged bearer token", h)
		}
	}
	// The token is exchanged once and reused across keyword queries.
	if stub.tokenCalls != 1 {
		t.Errorf("token exchanges = %d, want 1 (cached across the poll)", stub.tokenCalls)
	}
}

func TestRedditFetch_TokenCachedAcrossPolls(t *testing.T) {
	stub := newRedditStub(t, redditListingJSON)
	r := newTestReddit(stub, []string{"devops"}, []string{"datadog"})

	for i := 0; i < 3; i++ {
		if _, err := r.Fetch(context.Background()); err != nil {
			t.Fatalf("Fetch %d: %v", i, err)
		}
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenCalls != 1 {
		t.Errorf("token exchanges = %d over 3 polls, want 1 until expiry", stub.tokenCalls)
	}
}

// TestRedditFetch_401DropsToken asserts a rejected token is discarded rather
// than reused forever, so a rotated secret recovers on the next poll.
func TestRedditFetch_401DropsToken(t *testing.T) {
	stub := newRedditStub(t, redditListingJSON)
	stub.searchCode = http.StatusUnauthorized
	r := newTestReddit(stub, []string{"devops"}, []string{"datadog"})

	if _, err := r.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error on 401")
	}
	r.mu.Lock()
	tok := r.token
	r.mu.Unlock()
	if tok != "" {
		t.Errorf("token = %q after a 401, want it cleared for re-exchange", tok)
	}
}

func TestRedditFetch_PartialFailure(t *testing.T) {
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_token") {
			_, _ = w.Write([]byte(`{"access_token":"TKN","expires_in":3600}`))
			return
		}
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(redditListingJSON))
	}))
	defer srv.Close()

	r := NewReddit("cid", "csecret", []string{"devops"}, []string{"datadog", "mttr"})
	r.base = srv.URL
	r.tokenURL = srv.URL + "/api/v1/access_token"

	got, err := r.Fetch(context.Background())
	if err == nil {
		t.Error("expected the throttled query to be reported")
	}
	if len(got) == 0 {
		t.Error("a failing keyword query must not discard the surviving ones")
	}
}

func TestRedditFetch_NoSubreddits(t *testing.T) {
	r := NewReddit("cid", "csecret", nil, []string{"datadog"})
	if _, err := r.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error when no subreddits are configured")
	}
}

func TestRedditToken_BadCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := NewReddit("cid", "wrong", []string{"devops"}, []string{"datadog"})
	r.base = srv.URL
	r.tokenURL = srv.URL + "/api/v1/access_token"

	_, err := r.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected an error on a rejected token exchange")
	}
	// The error is for logs: it must not carry the app secret.
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("error leaks the client secret: %v", err)
	}
}

func TestRedditPrefiltersByKeyword(t *testing.T) {
	if !NewReddit("i", "s", []string{"devops"}, []string{"k"}).PrefiltersByKeyword() {
		t.Error("Reddit searches server-side by keyword, so it must claim the prefilter")
	}
	if NewReddit("i", "s", []string{"devops"}, nil).PrefiltersByKeyword() {
		t.Error("with no keywords there is no server-side narrowing to claim")
	}
}

func TestRedditTokenExpiry(t *testing.T) {
	stub := newRedditStub(t, redditListingJSON)
	r := newTestReddit(stub, []string{"devops"}, []string{"datadog"})

	if _, err := r.accessToken(context.Background()); err != nil {
		t.Fatalf("accessToken: %v", err)
	}
	// expires_in 86400 less the renewal skew: comfortably in the future.
	r.mu.Lock()
	till := r.tokenTill
	r.mu.Unlock()
	if time.Until(till) < 23*time.Hour {
		t.Errorf("token valid for %v, want ~24h less the renewal skew", time.Until(till))
	}
}
