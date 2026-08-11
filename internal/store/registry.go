package store

import (
	"errors"
	"fmt"

	"github.com/ImmersiveFusion/sos-beacon/internal/config"
	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

// ErrUnknownStore is returned by Build for an unregistered store type.
var ErrUnknownStore = errors.New("unknown store type")

// Constructor opens a Store from the global store config.
type Constructor func(cfg config.StoreConfig) (core.Store, error)

// registry maps a store type to its constructor. Adding a persistence backend
// is a new file in this package plus one line here. Explicit map, no init().
// Phase 0 ships the file store; azuresql is pulled forward for real-DB runs;
// sqlite lands later.
var registry = map[string]Constructor{
	"file":     func(cfg config.StoreConfig) (core.Store, error) { return OpenFile(cfg.Path) },
	"azuresql": func(cfg config.StoreConfig) (core.Store, error) { return OpenAzureSQL(cfg) },
}

// Build opens the Store for cfg.Type. It wraps ErrUnknownStore when the type is
// not registered.
func Build(cfg config.StoreConfig) (core.Store, error) {
	ctor, ok := registry[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStore, cfg.Type)
	}
	return ctor(cfg)
}
