// Package core holds the domain types and the four port interfaces
// (Fetcher, Classifier, Sink, Store) that the beacon pipeline consumes.
// It has no dependency on any other internal package: adapters depend on
// core, never the other way around.
package core

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// Signal is the normalized cross-platform schema every Source (Fetcher) emits.
type Signal struct {
	Source      string
	ID          string // source-native id; dedupe key with Source
	Title       string
	URL         string
	Author      string
	Body        string
	TopComments []string
	Score       int
	NumComments int
	CreatedAt   time.Time // freshness matters: HN dead 6-12h, Reddit 24-48h
	FetchedAt   time.Time
	Raw         json.RawMessage
}

// Bucket is a user-defined intent category for one beacon. Buckets are data,
// not code: the classifier is generic over whatever list a beacon config supplies.
type Bucket struct {
	Name       string `yaml:"name"`
	Emoji      string `yaml:"emoji"`
	Definition string `yaml:"definition"`
}

// Verdict is the classifier's output: a POINTER, never prose intended for
// posting. There is deliberately no path from a Verdict to a written reply.
type Verdict struct {
	Bucket    string             `json:"bucket"`
	Scores    map[string]float64 `json:"scores"`
	Fit       float64            `json:"fit"` // 0..1 topical relevance; drives thresholds
	Summary   string             `json:"summary"`
	Reasoning string             `json:"reasoning"`
}

// Finding is a Signal joined with its Verdict and the beacon that produced it.
type Finding struct {
	Beacon  string
	Signal  Signal
	Verdict Verdict
}

// BeaconContext is everything the classifier needs to judge a signal for one beacon.
type BeaconContext struct {
	Beacon  string
	Context string // topic / product context block, injected into the prompt
	Buckets []Bucket
}

// --- ports (interfaces the pipeline consumes; adapters implement them) ---

// Fetcher (Sources port) over-collects candidate signals from one platform.
type Fetcher interface {
	Name() string
	Fetch(ctx context.Context) ([]Signal, error)
}

// KeywordPrefilter is an optional Fetcher capability. A source that already
// narrowed its results by keyword server-side (a search API such as HN) may
// implement it and return true, so the engine skips the redundant client-side
// keyword pre-filter for that source's signals. Firehose sources (no server-
// side keyword query, e.g. Lobsters) do not implement it, so the pre-filter
// still gates them. This is what lets a keyword-queried source's hits reach the
// classifier even when the match was in the body or URL rather than the title.
type KeywordPrefilter interface {
	PrefiltersByKeyword() bool
}

// Classifier (AI port) sorts a signal into a beacon's buckets with scores.
type Classifier interface {
	Classify(ctx context.Context, s Signal, bc BeaconContext) (Verdict, error)
}

// Sink (Delivery port) posts a finding outward. Model text flows in here and
// stops; a Sink never writes into a public thread.
type Sink interface {
	Deliver(ctx context.Context, f Finding) error
}

// The Persistence port is deliberately split into narrow roles so each consumer
// can depend on only what it uses (interface segregation). The engine takes a
// Deduper and a Recorder; a claim bot would take a Ledger; nothing takes the
// fat Store except the wiring in main. Adapters (file, SQLite, Azure SQL)
// implement the whole Store and satisfy every narrow role for free.

// Deduper is the novelty set, scoped PER BEACON: has this beacon already
// processed (source, id)? Scoping by beacon means a post that one beacon rejects
// (or delivers) does not become invisible to another beacon that shares the same
// source, e.g. the Lobsters firehose that every beacon fetches identically.
type Deduper interface {
	Seen(beacon, source, id string) (bool, error)
	MarkSeen(beacon, source, id string) error
}

// Recorder persists a delivered or digest finding (and marks it seen).
type Recorder interface {
	Record(f Finding) error
}

// Ledger is the claim/attribution and digest-accumulator role.
type Ledger interface {
	Claim(findingID, claimant string) error
	PendingDigest(beacon string) ([]Finding, error)
}

// Store (Persistence port) is the full persistence contract: dedupe set, claim
// ledger, digest accumulator, and a Close for adapters that hold resources.
type Store interface {
	Deduper
	Recorder
	Ledger
	io.Closer
}
