package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func get(t *testing.T, m *Monitor, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	return rec.Code
}

func TestReadyz(t *testing.T) {
	m := New(time.Minute)
	if code := get(t, m, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("before Ready: got %d, want 503", code)
	}
	m.Ready()
	if code := get(t, m, "/readyz"); code != http.StatusOK {
		t.Fatalf("after Ready: got %d, want 200", code)
	}
}

func TestHealthzStaleness(t *testing.T) {
	clock := time.Unix(1000, 0)
	m := New(30 * time.Minute)
	m.now = func() time.Time { return clock }
	m.lastBeat.Store(clock.UnixNano()) // re-seed against the frozen clock
	m.Ready()

	// Fresh beat: healthy.
	if code := get(t, m, "/healthz"); code != http.StatusOK {
		t.Fatalf("fresh: got %d, want 200", code)
	}

	// Within the window: still healthy.
	clock = clock.Add(29 * time.Minute)
	if code := get(t, m, "/healthz"); code != http.StatusOK {
		t.Fatalf("within window: got %d, want 200", code)
	}

	// Past the window with no beat: stale -> unhealthy (a hung loop).
	clock = clock.Add(2 * time.Minute)
	if code := get(t, m, "/healthz"); code != http.StatusServiceUnavailable {
		t.Fatalf("stale: got %d, want 503", code)
	}

	// A beat clears it.
	m.Beat()
	if code := get(t, m, "/healthz"); code != http.StatusOK {
		t.Fatalf("after beat: got %d, want 200", code)
	}
}

func TestHealthzNotReady(t *testing.T) {
	m := New(time.Minute)
	// Beats arrive but startup never completed: not live yet.
	m.Beat()
	if code := get(t, m, "/healthz"); code != http.StatusServiceUnavailable {
		t.Fatalf("not ready: got %d, want 503", code)
	}
}
