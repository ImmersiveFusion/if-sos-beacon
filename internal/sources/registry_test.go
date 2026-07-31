package sources

import (
	"errors"
	"strings"
	"testing"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name     string
		wantName string
		wantErr  error
		beacon   config.BeaconConfig
		env      map[string]string
	}{
		{name: "hn", wantName: "hn"},
		{name: "lobsters", wantName: "lobsters"},
		{name: "reddit", wantName: "reddit", beacon: config.BeaconConfig{
			Keywords: []string{"k"},
			Reddit: config.RedditConfig{
				Subreddits:      []string{"devops"},
				ClientIDEnv:     "TEST_REDDIT_ID",
				ClientSecretEnv: "TEST_REDDIT_SECRET",
			},
		}, env: map[string]string{"TEST_REDDIT_ID": "id", "TEST_REDDIT_SECRET": "secret"}},
		{name: "nope", wantErr: ErrUnknownSource},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			beacon := tc.beacon
			if beacon.Keywords == nil {
				beacon.Keywords = []string{"k"}
			}
			f, err := Build(tc.name, beacon)
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
	want := []string{"hn", "lobsters", "reddit"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

// TestBuild_RedditPrerequisites asserts the constructor fails at wiring time,
// with a reason an operator can act on, rather than handing back a fetcher that
// errors identically on every poll forever.
func TestBuild_RedditPrerequisites(t *testing.T) {
	t.Run("no subreddits", func(t *testing.T) {
		t.Setenv("REDDIT_CLIENT_ID", "id")
		t.Setenv("REDDIT_CLIENT_SECRET", "secret")
		_, err := Build("reddit", config.BeaconConfig{
			Name:     "b",
			Keywords: []string{"k"},
			Reddit:   config.RedditConfig{ClientIDEnv: "REDDIT_CLIENT_ID", ClientSecretEnv: "REDDIT_CLIENT_SECRET"},
		})
		if err == nil {
			t.Fatal("expected an error when reddit is listed with no subreddits")
		}
	})

	t.Run("missing credentials", func(t *testing.T) {
		t.Setenv("REDDIT_CLIENT_ID", "")
		t.Setenv("REDDIT_CLIENT_SECRET", "")
		_, err := Build("reddit", config.BeaconConfig{
			Name:     "b",
			Keywords: []string{"k"},
			Reddit: config.RedditConfig{
				Subreddits:      []string{"devops"},
				ClientIDEnv:     "REDDIT_CLIENT_ID",
				ClientSecretEnv: "REDDIT_CLIENT_SECRET",
			},
		})
		if err == nil {
			t.Fatal("expected an error when the app credentials are empty")
		}
		if !strings.Contains(err.Error(), "REDDIT_CLIENT_ID") {
			t.Errorf("error should name the env var an operator must set: %v", err)
		}
	})
}
