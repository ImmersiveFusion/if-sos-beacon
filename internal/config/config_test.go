package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func TestLoad_AppliesDefaults(t *testing.T) {
	p := writeTemp(t, `
beacons:
  - name: sos-apm
    sources: [hn]
    destination: { type: discord, webhook_env: X }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AI.Provider != "openai-compatible" {
		t.Errorf("AI.Provider default = %q, want openai-compatible", cfg.AI.Provider)
	}
	if cfg.Store.Type != "file" || cfg.Store.Path != "./sos-state.json" {
		t.Errorf("store defaults = %q/%q", cfg.Store.Type, cfg.Store.Path)
	}
	b := cfg.Beacons[0]
	if b.Mode != "dispatch" {
		t.Errorf("mode default = %q, want dispatch", b.Mode)
	}
	if b.Thresholds.Realtime != 0.75 || b.Thresholds.Digest != 0.50 {
		t.Errorf("threshold defaults = %v/%v", b.Thresholds.Realtime, b.Thresholds.Digest)
	}
}

func TestLoad_NoBeaconsIsError(t *testing.T) {
	p := writeTemp(t, "ai: { model: gpt-x }\n")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error when config has no beacons")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing config file")
	}
}
