package delivery

import (
	"errors"
	"fmt"
	"os"

	"github.com/ImmersiveFusion/sos-beacon/internal/config"
	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

// Sentinel errors callers branch on: an unregistered destination type, or a
// registered one whose required secret env var is unset.
var (
	ErrUnknownDestination = errors.New("unknown destination")
	ErrMissingWebhook     = errors.New("destination webhook env is empty")
)

// Constructor builds a Sink from a beacon's destination config, resolving any
// secret (the webhook URL) from the environment itself.
type Constructor func(dest config.Destination) (core.Sink, error)

// registry maps a destination type to its constructor. Adding a delivery target
// is a new file in this package plus one line here. Explicit map, no init().
var registry = map[string]Constructor{
	"discord": func(dest config.Destination) (core.Sink, error) {
		webhook := os.Getenv(dest.WebhookEnv)
		if webhook == "" {
			return nil, fmt.Errorf("%w: %s", ErrMissingWebhook, dest.WebhookEnv)
		}
		return NewDiscord(webhook), nil
	},
}

// Build returns the Sink for dest.Type. It wraps ErrUnknownDestination for an
// unregistered type and ErrMissingWebhook when the secret env var is unset.
func Build(dest config.Destination) (core.Sink, error) {
	ctor, ok := registry[dest.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDestination, dest.Type)
	}
	return ctor(dest)
}
