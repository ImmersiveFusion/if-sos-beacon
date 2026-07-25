package sources

import (
	"errors"
	"testing"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name     string
		wantName string
		wantErr  error
	}{
		{name: "hn", wantName: "hn"},
		{name: "lobsters", wantName: "lobsters"},
		{name: "nope", wantErr: ErrUnknownSource},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Build(tc.name, config.BeaconConfig{Keywords: []string{"k"}})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build(%q): %v", tc.name, err)
			}
			if f.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", f.Name(), tc.wantName)
			}
		})
	}
}

func TestNames(t *testing.T) {
	got := Names()
	if len(got) != 2 || got[0] != "hn" || got[1] != "lobsters" {
		t.Errorf("Names() = %v, want [hn lobsters]", got)
	}
}
