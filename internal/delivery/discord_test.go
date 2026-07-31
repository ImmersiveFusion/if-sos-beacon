package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

func sampleFinding() core.Finding {
	return core.Finding{
		Beacon: "sos-apm",
		Signal: core.Signal{Source: "hn", ID: "1", Title: "Datadog bill", URL: "https://ex.com/a", Score: 42, NumComments: 7},
		Verdict: core.Verdict{
			Bucket:    "incumbent-rage",
			Fit:       0.91,
			Summary:   "MARKER_SUMMARY",
			Reasoning: "MARKER_REASONING",
		},
	}
}

func TestDeliver_EmbedShape(t *testing.T) {
	var mu sync.Mutex
	var payload struct {
		Embeds []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Color       int    `json:"color"`
		} `json:"embeds"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		err := json.Unmarshal(body, &payload)
		mu.Unlock()
		if err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
	if err := d.Deliver(context.Background(), sampleFinding()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(payload.Embeds) != 1 {
		t.Fatalf("embeds len = %d, want 1", len(payload.Embeds))
	}
	e := payload.Embeds[0]
	// The title carries the harvest source as a text prefix, so a reader can tell
	// the platform without opening the link.
	if e.Title != "[HN] Datadog bill" || e.URL != "https://ex.com/a" {
		t.Errorf("title/url = %q/%q", e.Title, e.URL)
	}
	if e.Color != 0xFF6600 {
		t.Errorf("color = %#x, want the HN stripe %#x", e.Color, 0xFF6600)
	}
	// The description is the pointer: bucket, fit, the model's summary + reasoning,
	// and the claim prompt. Model text flows here and stops.
	for _, want := range []string{"incumbent-rage", "fit 0.91", "MARKER_SUMMARY", "MARKER_REASONING", claimEmoji + " to claim", skipEmoji + " if spam"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("description missing %q: %q", want, e.Description)
		}
	}
}

// TestAttribution covers Developer Terms S5.2 for Reddit-sourced findings: the
// username must be cited and the platform named. The link-back is the embed URL,
// asserted in TestDeliver_EmbedShape.
func TestAttribution(t *testing.T) {
	tests := []struct {
		source, author, want string
	}{
		{"reddit", "alice", "u/alice on Reddit"},
		{"hn", "pg", "pg on Hacker News"},
		{"lobsters", "bob", "bob on Lobsters"},
		// Unregistered source: name it rather than dropping provenance.
		{"mastodon", "carol", "carol on mastodon"},
		// No author: omit the segment rather than render a dangling platform.
		{"reddit", "", ""},
	}
	for _, tc := range tests {
		if got := attribution(tc.source, tc.author); got != tc.want {
			t.Errorf("attribution(%q, %q) = %q, want %q", tc.source, tc.author, got, tc.want)
		}
	}
}

func TestBuildDescription_CitesRedditAuthor(t *testing.T) {
	f := sampleFinding()
	f.Signal.Source = "reddit"
	f.Signal.Author = "alice"
	got := buildDescription(f)
	if !strings.Contains(got, "u/alice on Reddit") {
		t.Errorf("description must cite the Reddit username and platform (S5.2): %q", got)
	}
}

func TestEmbedTitle_SourcePrefix(t *testing.T) {
	tests := []struct {
		name   string
		signal core.Signal
		want   string
	}{
		{"known source", core.Signal{Source: "hn", Title: "Datadog bill"}, "[HN] Datadog bill"},
		{"lobsters", core.Signal{Source: "lobsters", Title: "The mean means nothing"}, "[LB] The mean means nothing"},
		// An adapter with no table entry must still be labeled, never blank.
		{"unregistered source", core.Signal{Source: "mastodon", Title: "x"}, "[MASTODON] x"},
		{"no title falls back to id", core.Signal{Source: "hn", ID: "4242"}, "[HN] 4242"},
	}
	for _, tc := range tests {
		if got := embedTitle(tc.signal); got != tc.want {
			t.Errorf("%s: embedTitle = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestEmbedTitle_Truncates guards the cap that would otherwise make Discord
// reject the entire post rather than trim the title for us.
func TestEmbedTitle_Truncates(t *testing.T) {
	got := embedTitle(core.Signal{Source: "hn", Title: strings.Repeat("x", 400)})
	if n := len([]rune(got)); n != discordTitleLimit {
		t.Errorf("title rune length = %d, want %d", n, discordTitleLimit)
	}
	if !strings.HasPrefix(got, "[HN] ") {
		t.Errorf("truncation dropped the source prefix: %q", got[:16])
	}
}

func TestDeliver_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
	if err := d.Deliver(context.Background(), sampleFinding()); err == nil {
		t.Fatal("expected error on 400 status")
	}
}

func TestDeliver_PacesPosts(t *testing.T) {
	var mu sync.Mutex
	var recv []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		recv = append(recv, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	const spacing = 50 * time.Millisecond
	d := newDiscordWithSpacing(srv.URL, spacing)
	for i := 0; i < 3; i++ {
		if err := d.Deliver(context.Background(), sampleFinding()); err != nil {
			t.Fatalf("Deliver %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recv) != 3 {
		t.Fatalf("got %d posts, want 3", len(recv))
	}
	// The first post is immediate; each subsequent one is spaced. Allow slack.
	for i := 1; i < len(recv); i++ {
		if gap := recv[i].Sub(recv[i-1]); gap < spacing-10*time.Millisecond {
			t.Errorf("post %d followed post %d by %v, want >= ~%v", i, i-1, gap, spacing)
		}
	}
}

func TestRedactURL_StripsToken(t *testing.T) {
	secret := "https://discord.com/api/webhooks/123/SUPERSECRETTOKEN"
	wrapped := &url.Error{Op: "Post", URL: secret, Err: errors.New("dial tcp: refused")}

	got := redactURL(wrapped)
	if strings.Contains(got.Error(), "SUPERSECRETTOKEN") {
		t.Fatalf("redactURL leaked the token: %q", got)
	}
	if !strings.Contains(got.Error(), "dial tcp") {
		t.Errorf("redactURL dropped the underlying cause: %q", got)
	}
}

func TestDeliver_TransportErrorRedacted(t *testing.T) {
	// Point at a closed server so the client transport fails; assert the token
	// in the webhook path never reaches the returned error.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // now connections are refused

	webhook := fmt.Sprintf("%s/api/webhooks/999/TOKEN_LEAK_CANARY", base)
	d := NewDiscord(webhook)
	err := d.Deliver(context.Background(), sampleFinding())
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "TOKEN_LEAK_CANARY") {
		t.Fatalf("error leaked webhook token: %q", err)
	}
}
