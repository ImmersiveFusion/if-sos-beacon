// Package config loads the multi-beacon YAML config. Global `ai` and `store`
// blocks configure the AI and Persistence port adapters; each entry in
// `beacons` is one topic instance (its own sources, filter, buckets, channel).
package config

import (
	"fmt"
	"os"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
	"gopkg.in/yaml.v3"
)

// Config is the whole file: shared backends plus N beacons.
type Config struct {
	AI      AIConfig       `yaml:"ai"`
	Store   StoreConfig    `yaml:"store"`
	Beacons []BeaconConfig `yaml:"beacons"`
}

// AIConfig selects and configures the AI port adapter. The default
// `openai-compatible` provider covers Azure OpenAI, OpenAI, Ollama, vLLM,
// OpenRouter and Groq via base_url + key + model.
type AIConfig struct {
	Provider   string `yaml:"provider"`     // openai-compatible | anthropic | tessa
	BaseURLEnv string `yaml:"base_url_env"` // env var holding the API base URL
	Model      string `yaml:"model"`
	KeyEnv     string `yaml:"key_env"`     // env var holding the API key
	APIVersion string `yaml:"api_version"` // set for Azure OpenAI (uses api-key header + api-version)
}

// StoreConfig selects the Persistence port adapter.
type StoreConfig struct {
	Type string `yaml:"type"` // file | sqlite | azuresql
	Path string `yaml:"path"` // for file / sqlite
	DSN  string `yaml:"dsn"`  // for azuresql
}

// RedditConfig is per-beacon Reddit scoping (used from Phase 1).
type RedditConfig struct {
	Subreddits []string `yaml:"subreddits"`
}

// Destination selects the Delivery port adapter for one beacon.
type Destination struct {
	Type       string `yaml:"type"`        // discord | slack | email | webhook
	WebhookEnv string `yaml:"webhook_env"` // env var holding the webhook URL
}

// Thresholds gate delivery: fit >= realtime posts now, >= digest batches, below drops.
type Thresholds struct {
	Realtime float64 `yaml:"realtime"`
	Digest   float64 `yaml:"digest"`
}

// BeaconConfig is one topic instance.
type BeaconConfig struct {
	Name         string        `yaml:"name"`
	Mode         string        `yaml:"mode"` // dispatch | watch
	Sources      []string      `yaml:"sources"`
	Reddit       RedditConfig  `yaml:"reddit"`
	Filter       string        `yaml:"filter"` // boolean AND/OR/NOT (Phase 1)
	Keywords     []string      `yaml:"keywords"`
	Context      string        `yaml:"context"`
	Buckets      []core.Bucket `yaml:"buckets"`
	Destination  Destination   `yaml:"destination"`
	Thresholds   Thresholds    `yaml:"thresholds"`
	MaxAge       string        `yaml:"max_age"`       // e.g. "12h"; empty = no freshness gate
	PollInterval string        `yaml:"poll_interval"` // container loop mode (Phase 2)
}

// Load reads and validates the config, applying defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: config path is an operator-provided CLI flag, not attacker input
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if len(c.Beacons) == 0 {
		return nil, fmt.Errorf("config has no beacons")
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.AI.Provider == "" {
		c.AI.Provider = "openai-compatible"
	}
	if c.Store.Type == "" {
		c.Store.Type = "file"
	}
	if c.Store.Path == "" {
		c.Store.Path = "./sos-state.json"
	}
	for i := range c.Beacons {
		b := &c.Beacons[i]
		if b.Mode == "" {
			b.Mode = "dispatch"
		}
		if len(b.Sources) == 0 {
			b.Sources = []string{"hn"}
		}
		if b.Thresholds.Realtime == 0 {
			b.Thresholds.Realtime = 0.75
		}
		if b.Thresholds.Digest == 0 {
			b.Thresholds.Digest = 0.50
		}
	}
}
