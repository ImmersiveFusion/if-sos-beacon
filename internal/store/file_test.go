package store

import (
	"path/filepath"
	"testing"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

func TestFileStore_SeenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	seen, err := f.Seen("hn", "1")
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if seen {
		t.Fatal("fresh store should not report seen")
	}
	if err := f.MarkSeen("hn", "1"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	// Reopen from disk: the mark must have persisted (atomic save).
	f2, err := OpenFile(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	seen, err = f2.Seen("hn", "1")
	if err != nil {
		t.Fatalf("Seen after reopen: %v", err)
	}
	if !seen {
		t.Fatal("mark did not persist across reopen")
	}
}

func TestFileStore_RecordMarksSeenAndAccumulates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	fnd := core.Finding{
		Beacon:  "sos-apm",
		Signal:  core.Signal{Source: "hn", ID: "42"},
		Verdict: core.Verdict{Bucket: "seeker", Fit: 0.8},
	}
	if err := f.Record(fnd); err != nil {
		t.Fatalf("Record: %v", err)
	}

	seen, _ := f.Seen("hn", "42")
	if !seen {
		t.Error("Record should mark the signal seen")
	}

	pending, err := f.PendingDigest("sos-apm")
	if err != nil {
		t.Fatalf("PendingDigest: %v", err)
	}
	if len(pending) != 1 || pending[0].Signal.ID != "42" {
		t.Errorf("PendingDigest = %+v, want the recorded finding", pending)
	}
	if other, _ := f.PendingDigest("sos-ai"); len(other) != 0 {
		t.Errorf("PendingDigest for another beacon should be empty, got %+v", other)
	}
}

func TestFileStore_ImplementsStore(t *testing.T) {
	// Compile-time assurance the file adapter satisfies the full fat interface
	// (and therefore every narrow role the engine depends on).
	var _ core.Store = (*File)(nil)
}
