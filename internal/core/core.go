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
	URL         string // submitted/external link (may be empty for text posts); reference only
	Permalink   string // discussion thread where a human replies; the actionable link the beacon posts
	Author      string
	Body        string
	Tags        []string // source-native topic tags where the platform has them (Lobsters, dev.to, Stack Exchange); nil elsewhere
	TopComments []string
	Score       int
	NumComments int
	CreatedAt   time.Time // freshness matters: HN dead 6-12h, Reddit 24-48h
	FetchedAt   time.Time
	Raw         json.RawMessage

	// Prenarrowed marks a signal the SOURCE already narrowed to the beacon's
	// topic, so the engine skips the client-side keyword gate for it.
	//
	// This is per signal, not per fetcher (see KeywordPrefilter), because a
	// source can return both kinds in one fetch: Lobsters polls the global
	// newest feed (a firehose, gate it) and the beacon's own tag-scoped feeds
	// (the operator already declared those tags on-topic, do not gate them).
	// Without this, the only way to let tag-scoped results through would be to
	// list the tag names as `keywords`, which would also spray them at the
	// keyword-searching sources as extra queries.
	Prenarrowed bool
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

// Purger deletes everything the store holds that came from one source, across
// every beacon, and reports how many rows went.
//
// This exists because some platforms make deletion a term of access rather than
// a courtesy: Reddit's Data API Terms S6 lets Reddit revoke access at any time
// without notice, and S3.2/S6 then require deleting stored content and anything
// derived from it. A store that commingles dedupe rows, verdicts and the ledger
// across sources turns that into hand-written SQL under time pressure. One
// source-scoped delete path, built alongside the fetcher, keeps it a command.
//
// Purging is deliberately whole-source rather than per-post: revocation is not
// selective, and a partial purge is the failure mode worth designing out.
type Purger interface {
	PurgeSource(source string) (int, error)
}

// Store (Persistence port) is the full persistence contract: dedupe set, claim
// ledger, digest accumulator, and a Close for adapters that hold resources.
type Store interface {
	Deduper
	Recorder
	Ledger
	Purger
	io.Closer
}
