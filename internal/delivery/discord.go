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
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// Discord is a Sink that posts a finding as a Discord webhook embed.
type Discord struct {
	webhookURL string
	client     *http.Client
}

// NewDiscord builds a Discord sink for one webhook URL.
func NewDiscord(webhookURL string) *Discord {
	return &Discord{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 20 * time.Second},
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
	payload := discordPayload{
		Embeds: []discordEmbed{{
			Title:       embedTitle(f.Signal),
			URL:         f.Signal.URL,
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

func embedTitle(s core.Signal) string {
	if s.Title != "" {
		return s.Title
	}
	return s.Source + ":" + s.ID
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
		"🙋 to claim",
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
