package store

import (
	"path/filepath"
	"testing"

	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

func TestFileStore_SeenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	seen, err := f.Seen("b", "hn", "1")
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if seen {
		t.Fatal("fresh store should not report seen")
	}
	if err := f.MarkSeen("b", "hn", "1"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	// Reopen from disk: the mark must have persisted (atomic save).
	f2, err := OpenFile(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	seen, err = f2.Seen("b", "hn", "1")
	if err != nil {
		t.Fatalf("Seen after reopen: %v", err)
	}
	if !seen {
		t.Fatal("mark did not persist across reopen")
	}
}

func TestFileStore_SeenIsPerBeacon(t *testing.T) {
	f, err := OpenFile(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := f.MarkSeen("beacon-a", "lobsters", "shared"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if seen, _ := f.Seen("beacon-a", "lobsters", "shared"); !seen {
		t.Error("beacon-a should see its own mark")
	}
	if seen, _ := f.Seen("beacon-b", "lobsters", "shared"); seen {
		t.Error("beacon-b must NOT inherit beacon-a's mark for the same (source, id)")
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

	seen, _ := f.Seen("sos-apm", "hn", "42")
	if !seen {
		t.Error("Record should mark the signal seen for its beacon")
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

// TestFileStore_PurgeSource covers the deletion-on-revocation path: everything
// from one source goes, across every beacon, and nothing from another source is
// touched. Whole-source, because revocation is not selective.
func TestFileStore_PurgeSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	// Two beacons, two sources, both dedupe rows and recorded findings.
	if err := f.MarkSeen("sos-apm", "reddit", "t3_a"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if err := f.MarkSeen("sos-ai", "reddit", "t3_b"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if err := f.MarkSeen("sos-apm", "hn", "111"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if err := f.Record(core.Finding{Beacon: "sos-apm", Signal: core.Signal{Source: "reddit", ID: "t3_c", Title: "USER CONTENT"}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := f.Record(core.Finding{Beacon: "sos-apm", Signal: core.Signal{Source: "hn", ID: "222", Title: "keep me"}}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	n, err := f.PurgeSource("reddit")
	if err != nil {
		t.Fatalf("PurgeSource: %v", err)
	}
	// 2 dedupe rows + 1 recorded finding (the third reddit row came in via Record).
	if n != 4 {
		t.Errorf("purged %d rows, want 4 (2 marked seen, 1 finding, 1 seen row from Record)", n)
	}

	for _, tc := range []struct{ beacon, source, id string }{
		{"sos-apm", "reddit", "t3_a"},
		{"sos-ai", "reddit", "t3_b"},
		{"sos-apm", "reddit", "t3_c"},
	} {
		if seen, _ := f.Seen(tc.beacon, tc.source, tc.id); seen {
			t.Errorf("%s/%s/%s still marked seen after purge", tc.beacon, tc.source, tc.id)
		}
	}
	if seen, _ := f.Seen("sos-apm", "hn", "111"); !seen {
		t.Error("purging reddit must not touch hn dedupe rows")
	}
	pending, _ := f.PendingDigest("sos-apm")
	if len(pending) != 1 || pending[0].Signal.Source != "hn" {
		t.Errorf("findings after purge = %+v, want only the hn one", pending)
	}

	// It must survive the round trip: a purge that only cleared memory would
	// leave the content on disk, which is the whole thing being avoided.
	reopened, err := OpenFile(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if seen, _ := reopened.Seen("sos-apm", "reddit", "t3_a"); seen {
		t.Error("purged row came back after reopening the store")
	}
	after, _ := reopened.PendingDigest("sos-apm")
	if len(after) != 1 {
		t.Errorf("findings after reopen = %+v, want only the hn one", after)
	}
}

func TestFileStore_PurgeSource_NoMatchIsNoop(t *testing.T) {
	f, err := OpenFile(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := f.MarkSeen("sos-apm", "hn", "111"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	n, err := f.PurgeSource("reddit")
	if err != nil || n != 0 {
		t.Errorf("PurgeSource on an unused source = (%d, %v), want (0, nil)", n, err)
	}
	if seen, _ := f.Seen("sos-apm", "hn", "111"); !seen {
		t.Error("a no-op purge must not disturb other sources")
	}
}

func TestFileStore_ImplementsStore(t *testing.T) {
	// Compile-time assurance the file adapter satisfies the full fat interface
	// (and therefore every narrow role the engine depends on).
	var _ core.Store = (*File)(nil)
}
