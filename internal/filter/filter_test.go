package filter

import (
	"testing"

	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

func TestMatch(t *testing.T) {
	sig := core.Signal{
		Title:       "Our Datadog bill exploded",
		Body:        "considering alternatives",
		TopComments: []string{"try Grafana"},
	}
	tests := []struct {
		name     string
		signal   core.Signal
		keywords []string
		want     bool
	}{
		{name: "no keywords opens the gate", signal: sig, keywords: nil, want: true},
		{name: "match in title, case-insensitive", signal: sig, keywords: []string{"datadog"}, want: true},
		{name: "match in body", signal: sig, keywords: []string{"alternatives"}, want: true},
		{name: "match in comments", signal: sig, keywords: []string{"grafana"}, want: true},
		{name: "no match", signal: sig, keywords: []string{"kubernetes"}, want: false},
		{name: "empty keyword entries ignored", signal: sig, keywords: []string{"", "kubernetes"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.signal, tc.keywords, ""); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}
