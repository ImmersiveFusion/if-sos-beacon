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

	// azuresql: either give a full DSN, or a server + database that the adapter
	// composes into a fedauth=ActiveDirectoryDefault DSN. A SOS_STORE_DSN env
	// var overrides both (the cluster secret-store path). Never put a real
	// server name or secret in a committed config (D12): these come from the
	// operator's own config or environment.
	DSN      string `yaml:"dsn"`
	Server   string `yaml:"server"`
	Database string `yaml:"database"`
}

// RedditConfig is per-beacon Reddit scoping and credential wiring. Reddit needs
// app-only OAuth (D6: unauthenticated access was shut off), so the beacon reads
// a client id and secret from the environment. Only the env var NAMES live in
// config; the values never touch a committed file.
type RedditConfig struct {
	Subreddits      []string `yaml:"subreddits"`
	ClientIDEnv     string   `yaml:"client_id_env"`     // default REDDIT_CLIENT_ID
	ClientSecretEnv string   `yaml:"client_secret_env"` // default REDDIT_CLIENT_SECRET
}

// LobstersConfig is per-beacon Lobsters scoping. Lobsters has no keyword search
// API, so tags are the only server-side narrowing available: they add a
// tag-scoped feed alongside the global newest feed. Listing the same tag names in
// the beacon's `keywords` also lets tag membership alone pass the pre-filter,
// which matters because most Lobsters stories are link posts with no body text.
type LobstersConfig struct {
	Tags []string `yaml:"tags"`
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
	Name         string         `yaml:"name"`
	Mode         string         `yaml:"mode"` // dispatch | watch
	Sources      []string       `yaml:"sources"`
	Reddit       RedditConfig   `yaml:"reddit"`
	Lobsters     LobstersConfig `yaml:"lobsters"`
	Filter       string         `yaml:"filter"` // boolean AND/OR/NOT (Phase 1)
	Keywords     []string       `yaml:"keywords"`
	Context      string         `yaml:"context"`
	Buckets      []core.Bucket  `yaml:"buckets"`
	Destination  Destination    `yaml:"destination"`
	Thresholds   Thresholds     `yaml:"thresholds"`
	MaxAge       string         `yaml:"max_age"`       // e.g. "12h"; empty = no freshness gate
	PollInterval string         `yaml:"poll_interval"` // container loop mode (Phase 2)
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
		// Credential env var NAMES only; a forker can point these at their own.
		if b.Reddit.ClientIDEnv == "" {
			b.Reddit.ClientIDEnv = "REDDIT_CLIENT_ID"
		}
		if b.Reddit.ClientSecretEnv == "" {
			b.Reddit.ClientSecretEnv = "REDDIT_CLIENT_SECRET"
		}
	}
}
