package ai

import (
	"errors"
	"fmt"
	"os"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// ErrUnknownProvider is returned by Build for an unregistered AI provider.
var ErrUnknownProvider = errors.New("unknown ai provider")

// Constructor builds a Classifier from the global AI config, resolving any
// secrets from the environment itself (the config only names the env vars).
type Constructor func(cfg config.AIConfig) (core.Classifier, error)

// registry maps a provider name to its constructor. Adding an AI backend is a
// new file in this package plus one line here. Explicit map, no init().
var registry = map[string]Constructor{
	"openai-compatible": func(cfg config.AIConfig) (core.Classifier, error) {
		return NewOpenAICompat(
			os.Getenv(cfg.BaseURLEnv),
			cfg.Model,
			os.Getenv(cfg.KeyEnv),
			cfg.APIVersion,
		), nil
	},
}

// Build returns the Classifier for cfg.Provider. It wraps ErrUnknownProvider
// when the provider is not registered.
func Build(cfg config.AIConfig) (core.Classifier, error) {
	ctor, ok := registry[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, cfg.Provider)
	}
	return ctor(cfg)
}
