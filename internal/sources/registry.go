package sources

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// ErrUnknownSource is returned by Build when a beacon lists a source name that
// is not registered. Callers branch on it (warn and skip, rather than fail).
var ErrUnknownSource = errors.New("unknown source")

// Constructor builds a Fetcher for one beacon from that beacon's config.
type Constructor func(b config.BeaconConfig) core.Fetcher

// registry maps a source name (as used in a beacon's `sources:` list) to its
// constructor. This is the plugin seam: adding a source is a new file in this
// package plus one line here. Explicit map, never init()-based registration, so
// the full set of sources is greppable in one place.
var registry = map[string]Constructor{
	"hn":       func(b config.BeaconConfig) core.Fetcher { return NewHN(b.Keywords) },
	"lobsters": func(b config.BeaconConfig) core.Fetcher { return NewLobsters() },
}

// Build returns the Fetcher registered under name, configured for beacon b.
// It wraps ErrUnknownSource when name is not registered.
func Build(name string, b config.BeaconConfig) (core.Fetcher, error) {
	ctor, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, name)
	}
	return ctor(b), nil
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
