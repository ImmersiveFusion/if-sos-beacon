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
	if e.Title != "Datadog bill" || e.URL != "https://ex.com/a" {
		t.Errorf("title/url = %q/%q", e.Title, e.URL)
	}
	// The description is the pointer: bucket, fit, the model's summary + reasoning,
	// and the claim prompt. Model text flows here and stops.
	for _, want := range []string{"incumbent-rage", "fit 0.91", "MARKER_SUMMARY", "MARKER_REASONING", "🙋 to claim"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("description missing %q: %q", want, e.Description)
		}
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
