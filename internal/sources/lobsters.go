package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// lobstersBase is the site root. Lobsters exposes JSON by suffixing .json to a
// page path: /newest.json (the global firehose) and /t/<tag>,<tag>.json (the same
// listing scoped to tags). There is deliberately no search call here: unlike HN's
// Algolia API, lobste.rs search is HTML only (search.json returns 400), so tags
// are the only server-side narrowing the platform offers.
const lobstersBase = "https://lobste.rs"

// lobstersMaxTags caps how many tags go into one /t/ request. Tag feeds accept a
// comma list; a very long one makes an unwieldy URL for no gain, since the
// response is capped at a page of stories either way.
const lobstersMaxTags = 12

// userAgent identifies the beacon to the APIs it polls. Lobsters in particular
// asks pollers to send a descriptive User-Agent.
const userAgent = "sos-beacon/0.1 (+https://github.com/ImmersiveFusion/if-sos-beacon)"

// Lobsters is a Fetcher over lobste.rs listings.
//
// It polls the global newest feed and, when the beacon configures tags, the
// tag-scoped feed as well, merging both and deduping by story id. The two are
// complementary: the global feed is a page of ALL newest stories (25 items,
// roughly a day of site-wide volume, which is what a 12h freshness gate wants),
// while the tag feed is a page of the newest stories WITHIN the topics the beacon
// cares about, so a relevant story is not pushed off the page by unrelated
// volume. Neither narrows by keyword, so the engine's pre-filter still gates
// everything this returns.
type Lobsters struct {
	base   string // site root; overridden in tests
	tags   []string
	client *http.Client
}

// NewLobsters builds a Lobsters fetcher. tags scope the extra tag feed
// (lobsters.tags in the beacon config); empty means the global newest feed only.
func NewLobsters(tags []string) *Lobsters {
	return &Lobsters{
		base:   lobstersBase,
		tags:   normalizeTags(tags),
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// normalizeTags lowercases, trims, drops blanks and duplicates, and caps the
// list, so a sloppy config cannot produce a malformed or absurd tag URL.
func normalizeTags(in []string) []string {
	var out []string
	seen := make(map[string]bool, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == lobstersMaxTags {
			break
		}
	}
	return out
}

// Name identifies this source in Signal.Source and dedupe keys.
func (l *Lobsters) Name() string { return "lobsters" }

// feed is one listing to poll, plus whether its results are already scoped to
// the beacon's topic by the operator's own tag choice.
type feed struct {
	url         string
	prenarrowed bool
}

// feeds returns the listings to poll this run: always the global newest feed (a
// firehose, so the engine still gates it by keyword), plus the tag-scoped feed
// when tags are configured (on-topic by construction, so it is not gated).
func (l *Lobsters) feeds() []feed {
	out := []feed{{url: l.base + "/newest.json"}}
	if len(l.tags) > 0 {
		out = append(out, feed{
			url:         l.base + "/t/" + strings.Join(l.tags, ",") + ".json",
			prenarrowed: true,
		})
	}
	return out
}

type lobstersStory struct {
	ShortID      string       `json:"short_id"`
	CreatedAt    string       `json:"created_at"`
	Title        string       `json:"title"`
	URL          string       `json:"url"`
	Score        int          `json:"score"`
	CommentCount int          `json:"comment_count"`
	Description  string       `json:"description_plain"`
	CommentsURL  string       `json:"comments_url"`
	Tags         []string     `json:"tags"`
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

// Fetch pulls every configured feed and normalizes the union to signals,
// deduping by story id (a story in a watched tag appears in both feeds). A feed
// that fails does not sink the others: the error is returned alongside whatever
// the remaining feeds produced, and the engine counts it and moves on.
func (l *Lobsters) Fetch(ctx context.Context) ([]core.Signal, error) {
	now := time.Now()
	// index maps a story id to its position in out, so a story met again in a
	// later feed can be UPGRADED rather than dropped. This matters: a recent
	// story appears in both the global feed and its tag feed, and if first-seen
	// simply won, the global (non-narrowed) copy would always shadow the
	// tag-scoped one, silently defeating the tag configuration entirely.
	index := make(map[string]int)
	var out []core.Signal
	var firstErr error

	for _, f := range l.feeds() {
		stories, err := l.get(ctx, f.url)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, st := range stories {
			if st.ShortID == "" {
				continue
			}
			if i, dup := index[st.ShortID]; dup {
				// Pre-narrowing is sticky: being in ANY configured tag feed
				// means the operator declared this story on topic.
				out[i].Prenarrowed = out[i].Prenarrowed || f.prenarrowed
				continue
			}
			index[st.ShortID] = len(out)

			var created time.Time
			if t, perr := time.Parse(time.RFC3339, st.CreatedAt); perr == nil {
				created = t.UTC()
			}
			out = append(out, core.Signal{
				Source:      l.Name(),
				ID:          st.ShortID,
				Title:       st.Title,
				URL:         st.URL,         // external link (empty for text posts); reference only
				Permalink:   st.CommentsURL, // the thread to reply in
				Author:      st.SubmitterRaw.Username,
				Body:        st.Description,
				Tags:        st.Tags, // most stories are link posts with an empty body: tags carry the topicality
				Score:       st.Score,
				NumComments: st.CommentCount,
				CreatedAt:   created,
				FetchedAt:   now,
				Prenarrowed: f.prenarrowed,
			})
		}
	}
	return out, firstErr
}

func (l *Lobsters) get(ctx context.Context, endpoint string) ([]lobstersStory, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := l.client.Do(req) //nolint:gosec // G107: host is the fixed lobste.rs base; only the tag path varies, and tags are normalized
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lobsters %s: unexpected status %d", endpoint, resp.StatusCode)
	}

	var stories []lobstersStory
	if err := json.NewDecoder(resp.Body).Decode(&stories); err != nil {
		return nil, fmt.Errorf("lobsters %s: decode: %w", endpoint, err)
	}
	return stories, nil
}
