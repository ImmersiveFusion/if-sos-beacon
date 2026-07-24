package main

import (
	"testing"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
)

func TestResolveInterval(t *testing.T) {
	tests := []struct {
		name    string
		beacon  config.BeaconConfig
		global  time.Duration
		want    time.Duration
		wantErr bool
	}{
		{name: "per-beacon overrides global", beacon: config.BeaconConfig{PollInterval: "5m"}, global: time.Hour, want: 5 * time.Minute},
		{name: "falls back to global", beacon: config.BeaconConfig{}, global: 30 * time.Minute, want: 30 * time.Minute},
		{name: "no interval anywhere", beacon: config.BeaconConfig{}, global: 0, want: 0},
		{name: "bad duration errors", beacon: config.BeaconConfig{PollInterval: "notaduration"}, global: 0, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveInterval(tc.beacon, tc.global)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveInterval() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveInterval: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStaggerOffset(t *testing.T) {
	tests := []struct {
		name           string
		index, count   int
		interval, want time.Duration
	}{
		{name: "single beacon: no offset", index: 0, count: 1, interval: 10 * time.Minute, want: 0},
		{name: "first of two: no offset", index: 0, count: 2, interval: 10 * time.Minute, want: 0},
		{name: "second of two: half", index: 1, count: 2, interval: 10 * time.Minute, want: 5 * time.Minute},
		{name: "second of four: quarter", index: 1, count: 4, interval: 10 * time.Minute, want: 150 * time.Second},
		{name: "third of four: half", index: 2, count: 4, interval: 10 * time.Minute, want: 5 * time.Minute},
		{name: "zero interval: no offset", index: 1, count: 2, interval: 0, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := staggerOffset(tc.index, tc.count, tc.interval); got != tc.want {
				t.Errorf("staggerOffset(%d,%d,%v) = %v, want %v", tc.index, tc.count, tc.interval, got, tc.want)
			}
		})
	}
}

func TestCoverageGap(t *testing.T) {
	tests := []struct {
		name             string
		interval, maxAge time.Duration
		want             bool
	}{
		{name: "interval below max_age is safe", interval: 30 * time.Minute, maxAge: 12 * time.Hour, want: false},
		{name: "interval equal to max_age gaps", interval: 12 * time.Hour, maxAge: 12 * time.Hour, want: true},
		{name: "interval beyond max_age gaps", interval: 24 * time.Hour, maxAge: 12 * time.Hour, want: true},
		{name: "no interval: no gap", interval: 0, maxAge: 12 * time.Hour, want: false},
		{name: "no max_age: no gap", interval: time.Hour, maxAge: 0, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := coverageGap(tc.interval, tc.maxAge); got != tc.want {
				t.Errorf("coverageGap(%v,%v) = %v, want %v", tc.interval, tc.maxAge, got, tc.want)
			}
		})
	}
}

func TestLoopMode(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *config.Config
		global time.Duration
		want   bool
	}{
		{
			name:   "global interval triggers loop",
			cfg:    &config.Config{Beacons: []config.BeaconConfig{{Name: "a"}}},
			global: time.Minute,
			want:   true,
		},
		{
			name:   "per-beacon poll_interval triggers loop",
			cfg:    &config.Config{Beacons: []config.BeaconConfig{{Name: "a"}, {Name: "b", PollInterval: "10m"}}},
			global: 0,
			want:   true,
		},
		{
			name:   "no intervals is one-shot",
			cfg:    &config.Config{Beacons: []config.BeaconConfig{{Name: "a"}}},
			global: 0,
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := loopMode(tc.cfg, tc.global); got != tc.want {
				t.Errorf("loopMode() = %v, want %v", got, tc.want)
			}
		})
	}
}
