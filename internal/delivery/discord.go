// Package delivery holds the Delivery-port adapters (Sink implementations).
// Model text flows into a Sink and STOPS here: a Sink posts a pointer (an embed
// linking to the original thread), never a reply into a public thread.
// Adapters are dependency-free (stdlib only).
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// defaultPostSpacing is the minimum gap between posts to one webhook. Discord
// webhooks rate-limit around 30 requests/minute; ~2s spacing stays well under
// that and turns a fruitful poll's clump of embeds into a steady trickle
// instead of a burst.
const defaultPostSpacing = 2 * time.Second

// Discord is a Sink that posts a finding as a Discord webhook embed. It paces
// its posts (minSpacing) so a poll that produces many findings does not dump
// them all at once or trip the webhook rate limit.
type Discord struct {
	webhookURL string
	client     *http.Client

	minSpacing time.Duration
	mu         sync.Mutex
	lastPost   time.Time
}

// NewDiscord builds a Discord sink for one webhook URL, with default pacing.
func NewDiscord(webhookURL string) *Discord {
	return newDiscordWithSpacing(webhookURL, defaultPostSpacing)
}

// newDiscordWithSpacing builds a Discord sink with an explicit post spacing.
// Tests use a small (or zero) spacing to stay fast.
func newDiscordWithSpacing(webhookURL string, spacing time.Duration) *Discord {
	return &Discord{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 20 * time.Second},
		minSpacing: spacing,
	}
}

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// Deliver posts one finding as an embed. The description is a compact pointer:
// bucket, fit, summary, reasoning, age, points, comments, and a claim prompt.
func (d *Discord) Deliver(ctx context.Context, f core.Finding) error {
	if err := d.pace(ctx); err != nil {
		return err
	}
	payload := discordPayload{
		Embeds: []discordEmbed{{
			Title:       embedTitle(f.Signal),
			URL:         threadLink(f.Signal),
			Description: buildDescription(f),
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return redactURL(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req) //nolint:gosec // G107: URL is the operator-supplied webhook, not attacker input
	if err != nil {
		return redactURL(err)
	}
	defer resp.Body.Close()
	// Discord webhooks return 204 No Content on success.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discord webhook status %d", resp.StatusCode)
	}
	return nil
}

// redactURL strips the request URL out of an *url.Error so a failed webhook
// call cannot leak its secret token into logs. A webhook URL embeds an auth
// token in its path; the default net/http error text includes the full URL.
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("discord %s: %w", ue.Op, ue.Err)
	}
	return err
}

// pace blocks until at least minSpacing has elapsed since the previous post,
// then records this post's time. It respects context cancellation while waiting.
// A single sink is called sequentially by one beacon, so the lock is light; it
// also makes the sink safe if that ever changes.
func (d *Discord) pace(ctx context.Context) error {
	d.mu.Lock()
	var wait time.Duration
	if d.minSpacing > 0 && !d.lastPost.IsZero() {
		if elapsed := time.Since(d.lastPost); elapsed < d.minSpacing {
			wait = d.minSpacing - elapsed
		}
	}
	d.mu.Unlock()

	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}

	d.mu.Lock()
	d.lastPost = time.Now()
	d.mu.Unlock()
	return nil
}

func embedTitle(s core.Signal) string {
	if s.Title != "" {
		return s.Title
	}
	return s.Source + ":" + s.ID
}

// threadLink is the discussion thread a human opens to reply: the source
// Permalink (HN item, Lobsters comments), never the submitted external URL (the
// vendor page). Falls back to URL only if a source ever leaves Permalink empty,
// so the embed is never a dead link.
func threadLink(s core.Signal) string {
	if s.Permalink != "" {
		return s.Permalink
	}
	return s.URL
}

func buildDescription(f core.Finding) string {
	v := f.Verdict
	parts := []string{
		fmt.Sprintf("**%s**", v.Bucket),
		fmt.Sprintf("fit %.2f", v.Fit),
	}
	if v.Summary != "" {
		parts = append(parts, v.Summary)
	}
	if v.Reasoning != "" {
		parts = append(parts, "_"+v.Reasoning+"_")
	}
	parts = append(parts,
		age(f.Signal),
		fmt.Sprintf("%d pts", f.Signal.Score),
		fmt.Sprintf("%d comments", f.Signal.NumComments),
		"🙋 to claim · ❌ if spam or not worth it",
	)
	return strings.Join(parts, " · ")
}

// age renders the elapsed time from CreatedAt to FetchedAt as a coarse label.
func age(s core.Signal) string {
	if s.CreatedAt.IsZero() {
		return "age ?"
	}
	end := s.FetchedAt
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(s.CreatedAt)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm old", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh old", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd old", int(d.Hours()/24))
	}
}
