// Package sources holds the Sources-port adapters (Fetcher implementations).
// Each adapter over-collects candidate signals from one platform and normalizes
// them to core.Signal. Adapters are dependency-free (stdlib only).
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// hnEndpoint is the Algolia HN Search "by date" API. We query per keyword and
// dedupe across keywords by objectID.
const hnEndpoint = "https://hn.algolia.com/api/v1/search_by_date"

// hnHitsPerPage caps results per keyword query.
const hnHitsPerPage = 20

// HN is a Fetcher over Hacker News via the public Algolia search API. No auth.
type HN struct {
	keywords []string
	endpoint string
	client   *http.Client
}

// NewHN builds an HN fetcher that queries once per keyword.
func NewHN(keywords []string) *HN {
	return &HN{
		keywords: keywords,
		endpoint: hnEndpoint,
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

// Name identifies this source in Signal.Source and dedupe keys.
func (h *HN) Name() string { return "hn" }

// hnResponse is the slice of the Algolia payload we consume.
type hnResponse struct {
	Hits []hnHit `json:"hits"`
}

type hnHit struct {
	ObjectID    string `json:"objectID"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Author      string `json:"author"`
	StoryText   string `json:"story_text"`
	Points      int    `json:"points"`
	NumComments int    `json:"num_comments"`
	CreatedAtI  int64  `json:"created_at_i"`
}

// Fetch queries HN once per keyword and returns deduped, normalized signals.
func (h *HN) Fetch(ctx context.Context) ([]core.Signal, error) {
	seen := make(map[string]bool)
	var out []core.Signal
	now := time.Now()

	for _, kw := range h.keywords {
		if kw == "" {
			continue
		}
		hits, err := h.query(ctx, kw)
		if err != nil {
			return out, fmt.Errorf("hn query %q: %w", kw, err)
		}
		for _, hit := range hits {
			if hit.ObjectID == "" || seen[hit.ObjectID] {
				continue
			}
			seen[hit.ObjectID] = true

			link := hit.URL
			if link == "" {
				link = "https://news.ycombinator.com/item?id=" + hit.ObjectID
			}
			out = append(out, core.Signal{
				Source:      h.Name(),
				ID:          hit.ObjectID,
				Title:       hit.Title,
				URL:         link,
				Author:      hit.Author,
				Body:        hit.StoryText,
				Score:       hit.Points,
				NumComments: hit.NumComments,
				CreatedAt:   time.Unix(hit.CreatedAtI, 0).UTC(),
				FetchedAt:   now,
			})
		}
	}
	return out, nil
}

func (h *HN) query(ctx context.Context, keyword string) ([]hnHit, error) {
	q := url.Values{}
	q.Set("tags", "story")
	q.Set("query", keyword)
	q.Set("hitsPerPage", strconv.Itoa(hnHitsPerPage))
	reqURL := h.endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req) //nolint:gosec // G107: host is the fixed Algolia endpoint; only the query string varies
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var r hnResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return r.Hits, nil
}
