package sources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// Reddit endpoints. Token exchange happens on www.reddit.com; every authenticated
// API call goes to oauth.reddit.com. Unauthenticated access to the JSON endpoints
// was shut off in 2023-2025, which is why OAuth is not optional here (D6).
const (
	//nolint:gosec // G101: this is Reddit's public token ENDPOINT, not a credential. The app id and secret come from the environment.
	redditTokenURL = "https://www.reddit.com/api/v1/access_token"
	redditAPIBase  = "https://oauth.reddit.com"
)

// redditLimit caps results per keyword query (Reddit's own max page is 100).
const redditLimit = 25

// redditSpacing paces successive Reddit calls. Authenticated clients get 100
// requests per minute averaged over a 10 minute window; one query per keyword
// means a beacon with 18 keywords issues 18 calls per poll, so a small fixed gap
// keeps a poll comfortably inside the budget without any token-bucket machinery.
const redditSpacing = 700 * time.Millisecond

// redditTokenSkew renews the app token slightly before it expires, so a long poll
// cannot start a request with a token that dies mid-flight.
const redditTokenSkew = 60 * time.Second

// Reddit is a Fetcher over Reddit's search API, scoped to a beacon's subreddits.
//
// It uses application-only OAuth (grant_type=client_credentials): the beacon
// reads public listings as itself, never as a user, so no account password is
// involved and nothing it does requires pretending to be a person (D7).
type Reddit struct {
	clientID     string
	clientSecret string
	subreddits   []string
	keywords     []string

	base     string // API base; overridden in tests
	tokenURL string // token endpoint; overridden in tests
	client   *http.Client

	mu        sync.Mutex
	token     string
	tokenTill time.Time
}

// NewReddit builds a Reddit fetcher for one beacon. Credentials come from the
// environment (resolved by the caller), never from config, so no secret is ever
// written into a committed beacon file.
func NewReddit(clientID, clientSecret string, subreddits, keywords []string) *Reddit {
	return &Reddit{
		clientID:     clientID,
		clientSecret: clientSecret,
		subreddits:   normalizeSubreddits(subreddits),
		keywords:     keywords,
		base:         redditAPIBase,
		tokenURL:     redditTokenURL,
		client:       &http.Client{Timeout: 20 * time.Second},
	}
}

// normalizeSubreddits accepts the forms an operator will actually write
// ("devops", "r/devops", "/r/devops", with stray case and spaces) and reduces
// them to the bare deduplicated names the multireddit path needs.
func normalizeSubreddits(in []string) []string {
	var out []string
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "/")
		s = strings.TrimPrefix(s, "r/")
		s = strings.Trim(s, "/")
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// Name identifies this source in Signal.Source and dedupe keys.
func (r *Reddit) Name() string { return "reddit" }

// PrefiltersByKeyword reports that Reddit narrowed results server-side via the
// search API, so the engine skips the redundant client-side keyword gate for
// Reddit signals (matching HN's behavior).
func (r *Reddit) PrefiltersByKeyword() bool { return len(r.keywords) > 0 }

// tokenResponse is the subset of the OAuth response we consume.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// accessToken returns a valid app-only bearer token, fetching a new one when the
// cached one is missing or near expiry. Concurrent callers share one exchange.
func (r *Reddit) accessToken(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.token != "" && time.Now().Before(r.tokenTill) {
		return r.token, nil
	}
	if r.clientID == "" || r.clientSecret == "" {
		return "", fmt.Errorf("reddit: missing client id or secret")
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	// Basic auth with the app credentials; Reddit rejects them in the body.
	basic := base64.StdEncoding.EncodeToString([]byte(r.clientID + ":" + r.clientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := r.client.Do(req) //nolint:gosec // G107: fixed token endpoint; credentials travel in headers
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The status is the whole diagnosis here (401 = bad app credentials,
		// 429 = throttled) and the body can echo the request, so only the code
		// is surfaced.
		return "", fmt.Errorf("reddit token: unexpected status %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("reddit token: decode: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("reddit token: empty access_token")
	}

	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= redditTokenSkew {
		ttl = redditTokenSkew * 2 // pathological/absent expiry: keep it short
	}
	r.token = tr.AccessToken
	r.tokenTill = time.Now().Add(ttl - redditTokenSkew)
	return r.token, nil
}

// redditListing is the slice of the listing payload we consume.
type redditListing struct {
	Data struct {
		Children []struct {
			Data redditPost `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

type redditPost struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"` // fullname, e.g. t3_abc123
	Title       string  `json:"title"`
	Selftext    string  `json:"selftext"`
	URL         string  `json:"url"`
	Permalink   string  `json:"permalink"` // site-relative
	Author      string  `json:"author"`
	Subreddit   string  `json:"subreddit"`
	Score       int     `json:"score"`
	NumComments int     `json:"num_comments"`
	CreatedUTC  float64 `json:"created_utc"`
	IsSelf      bool    `json:"is_self"`
	Over18      bool    `json:"over_18"`
	Stickied    bool    `json:"stickied"`
}

// Fetch queries the beacon's subreddits once per keyword and returns deduped,
// normalized signals. A single keyword's failure does not sink the poll: the
// first error is reported alongside everything the other queries produced.
func (r *Reddit) Fetch(ctx context.Context) ([]core.Signal, error) {
	if len(r.subreddits) == 0 {
		return nil, fmt.Errorf("reddit: no subreddits configured")
	}
	token, err := r.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	// One multireddit path covers every configured subreddit in a single call
	// per keyword, instead of keywords x subreddits calls.
	multi := strings.Join(r.subreddits, "+")

	now := time.Now()
	seen := make(map[string]bool)
	var out []core.Signal
	var firstErr error

	for i, kw := range r.keywords {
		if kw == "" {
			continue
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(redditSpacing):
			}
		}
		posts, qerr := r.search(ctx, token, multi, kw)
		if qerr != nil {
			if firstErr == nil {
				firstErr = qerr
			}
			continue
		}
		for _, p := range posts {
			id := p.Name
			if id == "" {
				id = p.ID
			}
			// Stickied posts are moderator furniture (rules, megathreads), not a
			// person with a problem, and they would re-surface on every poll.
			if id == "" || seen[id] || p.Stickied {
				continue
			}
			seen[id] = true

			link := p.URL
			if p.IsSelf {
				link = "" // self post: the thread IS the content, no external link
			}
			out = append(out, core.Signal{
				Source:      r.Name(),
				ID:          id,
				Title:       p.Title,
				URL:         link,
				Permalink:   "https://www.reddit.com" + p.Permalink, // the thread to reply in
				Author:      p.Author,
				Body:        p.Selftext,
				Tags:        []string{"r/" + p.Subreddit}, // the subreddit is this platform's topic tag
				Score:       p.Score,
				NumComments: p.NumComments,
				CreatedAt:   time.Unix(int64(p.CreatedUTC), 0).UTC(),
				FetchedAt:   now,
			})
		}
	}
	return out, firstErr
}

// search runs one keyword query restricted to the beacon's subreddits, newest
// first.
func (r *Reddit) search(ctx context.Context, token, multi, keyword string) ([]redditPost, error) {
	q := url.Values{}
	q.Set("q", keyword)
	q.Set("sort", "new")
	q.Set("limit", strconv.Itoa(redditLimit))
	q.Set("restrict_sr", "1") // stay inside the configured subreddits
	q.Set("raw_json", "1")    // no HTML entity escaping in the text fields
	q.Set("type", "link")     // posts, not comments or users

	endpoint := r.base + "/r/" + multi + "/search?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent) // Reddit blocks generic agents outright
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req) //nolint:gosec // G107: host is the fixed Reddit API base; only the query varies
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// The cached token was rejected: drop it so the next poll re-exchanges
		// rather than looping on a dead credential.
		r.mu.Lock()
		r.token, r.tokenTill = "", time.Time{}
		r.mu.Unlock()
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reddit search %q: unexpected status %d", keyword, resp.StatusCode)
	}

	var l redditListing
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return nil, fmt.Errorf("reddit search %q: decode: %w", keyword, err)
	}
	posts := make([]redditPost, 0, len(l.Data.Children))
	for _, c := range l.Data.Children {
		posts = append(posts, c.Data)
	}
	return posts, nil
}
