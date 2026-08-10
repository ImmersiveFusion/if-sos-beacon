package sources

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/ImmersiveFusion/sos-beacon/internal/config"
	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

// ErrUnknownSource is returned by Build when a beacon lists a source name that
// is not registered. Callers branch on it (warn and skip, rather than fail).
var ErrUnknownSource = errors.New("unknown source")

// Constructor builds a Fetcher for one beacon from that beacon's config. It may
// fail: a source with prerequisites (Reddit needs app credentials in the
// environment and at least one subreddit) reports that here, at wiring time, so
// the beacon logs one clear "skipping source" reason instead of the same fetch
// error on every poll forever.
type Constructor func(b config.BeaconConfig) (core.Fetcher, error)

// registry maps a source name (as used in a beacon's `sources:` list) to its
// constructor. This is the plugin seam: adding a source is a new file in this
// package plus one line here. Explicit map, never init()-based registration, so
// the full set of sources is greppable in one place.
var registry = map[string]Constructor{
	"hn": func(b config.BeaconConfig) (core.Fetcher, error) {
		return NewHN(b.Keywords), nil
	},
	"lobsters": func(b config.BeaconConfig) (core.Fetcher, error) {
		return NewLobsters(b.Lobsters.Tags), nil
	},
	"reddit": func(b config.BeaconConfig) (core.Fetcher, error) {
		if len(b.Reddit.Subreddits) == 0 {
			return nil, fmt.Errorf("reddit: beacon %q lists the reddit source but no reddit.subreddits", b.Name)
		}
		id, secret := os.Getenv(b.Reddit.ClientIDEnv), os.Getenv(b.Reddit.ClientSecretEnv)
		if id == "" || secret == "" {
			return nil, fmt.Errorf("reddit: %s / %s are empty (app-only OAuth is required; unauthenticated Reddit is gone)",
				b.Reddit.ClientIDEnv, b.Reddit.ClientSecretEnv)
		}
		return NewReddit(id, secret, b.Reddit.Subreddits, b.Keywords), nil
	},
}

// Build returns the Fetcher registered under name, configured for beacon b.
// It wraps ErrUnknownSource when name is not registered.
func Build(name string, b config.BeaconConfig) (core.Fetcher, error) {
	ctor, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, name)
	}
	return ctor(b)
}

// Names lists the registered source names, sorted, for diagnostics.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
