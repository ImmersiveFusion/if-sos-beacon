// Package store holds the Persistence-port adapters (Store implementations).
// The Store's job is narrow and shared across run modes: a dedupe set, a
// claim/attribution ledger, and a digest accumulator. The file adapter is the
// zero-infra default for one-shot runs; its state must round-trip between runs
// (committed, or cached as an Actions artifact) or the beacon re-posts.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

// File is a JSON-file-backed Store. All access is guarded by a mutex; every
// mutation persists atomically (write temp, fsync-free rename).
type File struct {
	path string
	mu   sync.Mutex
	data state
}

// state is the on-disk shape. Keys in Seen are "source:id".
type state struct {
	Seen     map[string]bool   `json:"seen"`
	Findings []core.Finding    `json:"findings"`
	Claims   map[string]string `json:"claims"`
}

// OpenFile loads (or initializes) the store at path.
func OpenFile(path string) (*File, error) {
	f := &File{
		path: path,
		data: state{
			Seen:   make(map[string]bool),
			Claims: make(map[string]string),
		},
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: store path is operator config, not attacker input
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil // fresh store
		}
		return nil, fmt.Errorf("read store: %w", err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &f.data); err != nil {
			return nil, fmt.Errorf("parse store: %w", err)
		}
	}
	if f.data.Seen == nil {
		f.data.Seen = make(map[string]bool)
	}
	if f.data.Claims == nil {
		f.data.Claims = make(map[string]string)
	}
	return f, nil
}

func key(beacon, source, id string) string { return beacon + ":" + source + ":" + id }

// Seen reports whether the beacon has already processed (source, id).
func (f *File) Seen(beacon, source, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data.Seen[key(beacon, source, id)], nil
}

// MarkSeen records (source, id) as processed for the beacon, without delivering.
func (f *File) MarkSeen(beacon, source, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.Seen[key(beacon, source, id)] = true
	return f.saveLocked()
}

// Record stores a delivered/digest finding and marks its signal seen for the
// finding's beacon.
func (f *File) Record(fnd core.Finding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.Findings = append(f.data.Findings, fnd)
	f.data.Seen[key(fnd.Beacon, fnd.Signal.Source, fnd.Signal.ID)] = true
	return f.saveLocked()
}

// Claim records that claimant has taken responsibility for a finding.
func (f *File) Claim(findingID, claimant string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.Claims[findingID] = claimant
	return f.saveLocked()
}

// PendingDigest returns the findings recorded for one beacon (the digest
// accumulator). Phase 0 records but does not yet flush a digest.
func (f *File) PendingDigest(beacon string) ([]core.Finding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Finding
	for _, fnd := range f.data.Findings {
		if fnd.Beacon == beacon {
			out = append(out, fnd)
		}
	}
	return out, nil
}

// PurgeSource deletes every dedupe row and recorded finding that came from one
// source, across all beacons, and reports how many rows went (core.Purger).
//
// The claim ledger is keyed by an opaque finding id supplied by a caller, so it
// is not source-mappable here. Nothing writes it today (the claim bot is a later
// phase), and it holds only the claimant's own name, never fetched content. When
// that bot lands it must key claims by (source, id) so this can clear them too.
func (f *File) PurgeSource(source string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	// Keys are "beacon:source:id"; match the middle segment exactly rather than
	// substring-matching, so a source named "hn" cannot delete "hn-mirror" rows.
	for k := range f.data.Seen {
		parts := strings.Split(k, ":")
		if len(parts) >= 3 && parts[1] == source {
			delete(f.data.Seen, k)
			n++
		}
	}
	kept := f.data.Findings[:0]
	for _, fnd := range f.data.Findings {
		if fnd.Signal.Source == source {
			n++
			continue
		}
		kept = append(kept, fnd)
	}
	f.data.Findings = kept

	if n == 0 {
		return 0, nil // nothing to write
	}
	return n, f.saveLocked()
}

// Close is a no-op for the file store (every mutation already persisted).
func (f *File) Close() error { return nil }

// saveLocked writes the state atomically. Caller must hold f.mu.
func (f *File) saveLocked() error {
	b, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal store: %w", err)
	}
	dir := filepath.Dir(f.path)
	tmp, err := os.CreateTemp(dir, ".sos-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}
