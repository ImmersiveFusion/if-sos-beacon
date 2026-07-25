package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// lobstersEndpoint is the public "newest stories" JSON feed. No auth. It is a
// firehose across all topics, so the beacon's keyword pre-filter does the
// narrowing downstream (unlike HN, which is queried per keyword).
const lobstersEndpoint = "https://lobste.rs/newest.json"

// userAgent identifies the beacon to the APIs it polls. Lobsters in particular
// asks pollers to send a descriptive User-Agent.
const userAgent = "sos-beacon/0.1 (+https://github.com/ImmersiveFusion/if-sos-beacon)"

// Lobsters is a Fetcher over the lobste.rs newest-stories feed.
type Lobsters struct {
	endpoint string
	client   *http.Client
}

// NewLobsters builds a Lobsters fetcher. It takes no per-beacon options: the
// feed is unfiltered, so keyword narrowing happens in the pre-filter stage.
func NewLobsters() *Lobsters {
	return &Lobsters{
		endpoint: lobstersEndpoint,
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

// Name identifies this source in Signal.Source and dedupe keys.
func (l *Lobsters) Name() string { return "lobsters" }

type lobstersStory struct {
	ShortID      string       `json:"short_id"`
	CreatedAt    string       `json:"created_at"`
	Title        string       `json:"title"`
	URL          string       `json:"url"`
	Score        int          `json:"score"`
	CommentCount int          `json:"comment_count"`
	Description  string       `json:"description_plain"`
	CommentsURL  string       `json:"comments_url"`
	SubmitterRaw lobstersUser `json:"submitter_user"`
}

// lobstersUser tolerates both shapes lobste.rs has used for submitter_user: a
// bare username string, and an object with a "username" field.
type lobstersUser struct {
	Username string
}

func (u *lobstersUser) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		u.Username = s
		return nil
	}
	var o struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	u.Username = o.Username
	return nil
}

// Fetch pulls the newest stories and normalizes them to signals.
func (l *Lobsters) Fetch(ctx context.Context) ([]core.Signal, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := l.client.Do(req) //nolint:gosec // G107: endpoint is a fixed constant, not user input
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lobsters: unexpected status %d", resp.StatusCode)
	}

	var stories []lobstersStory
	if err := json.NewDecoder(resp.Body).Decode(&stories); err != nil {
		return nil, fmt.Errorf("lobsters: decode: %w", err)
	}

	now := time.Now()
	out := make([]core.Signal, 0, len(stories))
	for _, st := range stories {
		if st.ShortID == "" {
			continue
		}
		link := st.URL
		if link == "" {
			link = st.CommentsURL
		}
		var created time.Time
		if t, perr := time.Parse(time.RFC3339, st.CreatedAt); perr == nil {
			created = t.UTC()
		}
		out = append(out, core.Signal{
			Source:      l.Name(),
			ID:          st.ShortID,
			Title:       st.Title,
			URL:         link,
			Author:      st.SubmitterRaw.Username,
			Body:        st.Description,
			Score:       st.Score,
			NumComments: st.CommentCount,
			CreatedAt:   created,
			FetchedAt:   now,
		})
	}
	return out, nil
}
