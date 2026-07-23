// Package engine is the pure pipeline that turns fetched signals into delivered
// findings. It knows nothing about HN, Discord, or any model: it consumes the
// four ports through Deps. The pipeline is the only place the anti-slop rule is
// enforced structurally: a Verdict flows to the Sink and stops.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/filter"
)

// Deps bundles the port adapters one beacon run needs. Persistence is taken as
// the two narrow roles the pipeline actually uses (dedupe + record), not the
// fat Store: the engine cannot touch the claim ledger even by accident.
type Deps struct {
	Fetchers   []core.Fetcher
	Classifier core.Classifier
	Sink       core.Sink
	Deduper    core.Deduper
	Recorder   core.Recorder
}

// Stats summarizes one beacon run.
type Stats struct {
	Fetched    int // signals returned by all fetchers
	Stale      int // dropped by the freshness gate
	Seen       int // skipped as already processed
	Filtered   int // dropped by the keyword/boolean pre-filter
	Classified int // signals sent to the classifier
	Delivered  int // posted in realtime
	Digest     int // batched (accumulated, not posted this run)
	Dropped    int // noise or below the digest threshold
	Errors     int // fetch/classify/deliver errors (signal retried next run)
}

// RunBeacon executes the full pipeline for one beacon.
func RunBeacon(ctx context.Context, cfg config.BeaconConfig, deps Deps, log *slog.Logger) (Stats, error) {
	var st Stats

	if deps.Deduper == nil {
		return st, errors.New("engine: nil deduper")
	}
	if deps.Recorder == nil {
		return st, errors.New("engine: nil recorder")
	}
	if deps.Classifier == nil {
		return st, errors.New("engine: nil classifier")
	}
	if deps.Sink == nil {
		return st, errors.New("engine: nil sink")
	}

	// max_age freshness gate (empty = no gate).
	var maxAge time.Duration
	if cfg.MaxAge != "" {
		d, err := time.ParseDuration(cfg.MaxAge)
		if err != nil {
			return st, err
		}
		maxAge = d
	}

	bc := core.BeaconContext{
		Beacon:  cfg.Name,
		Context: cfg.Context,
		Buckets: cfg.Buckets,
	}

	// --- fetch (over-collect from every source) ---
	var signals []core.Signal
	for _, f := range deps.Fetchers {
		got, err := f.Fetch(ctx)
		if err != nil {
			st.Errors++
			log.Warn("fetch failed", "beacon", cfg.Name, "source", f.Name(), "err", err)
		}
		signals = append(signals, got...)
	}
	st.Fetched = len(signals)

	now := time.Now()
	for _, s := range signals {
		// --- freshness ---
		if maxAge > 0 && !s.CreatedAt.IsZero() && now.Sub(s.CreatedAt) > maxAge {
			st.Stale++
			continue
		}

		// --- novelty (dedupe) ---
		seen, err := deps.Deduper.Seen(s.Source, s.ID)
		if err != nil {
			st.Errors++
			log.Warn("seen check failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			continue
		}
		if seen {
			st.Seen++
			continue
		}

		// --- pre-filter (cheap, deterministic) ---
		if !filter.Match(s, cfg.Keywords, cfg.Filter) {
			st.Filtered++
			if err := deps.Deduper.MarkSeen(s.Source, s.ID); err != nil {
				log.Warn("mark seen failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			}
			continue
		}

		// --- classify (LLM) ---
		st.Classified++
		v, err := deps.Classifier.Classify(ctx, s, bc)
		if err != nil {
			// No MarkSeen: leave it to retry on the next run.
			st.Errors++
			log.Warn("classify failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			continue
		}

		// --- decide ---
		if v.Bucket == "noise" || v.Fit < cfg.Thresholds.Digest {
			st.Dropped++
			if err := deps.Deduper.MarkSeen(s.Source, s.ID); err != nil {
				log.Warn("mark seen failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			}
			continue
		}

		fnd := core.Finding{Beacon: cfg.Name, Signal: s, Verdict: v}

		if v.Fit >= cfg.Thresholds.Realtime {
			if err := deps.Sink.Deliver(ctx, fnd); err != nil {
				// No Record/MarkSeen: retry delivery next run.
				st.Errors++
				log.Warn("deliver failed", "beacon", cfg.Name, "id", s.ID, "err", err)
				continue
			}
			st.Delivered++
		} else {
			st.Digest++
		}

		// Record marks the signal seen and accumulates it for the digest.
		if err := deps.Recorder.Record(fnd); err != nil {
			st.Errors++
			log.Warn("record failed", "beacon", cfg.Name, "id", s.ID, "err", err)
		}
	}

	return st, nil
}
