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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/filter"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/telemetry"
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

	// SourceSpacing, when > 0, inserts a pause between successive source fetches
	// so a beacon with several sources does not fetch them all in one burst.
	// Zero (the one-shot default) fetches back-to-back.
	SourceSpacing time.Duration
}

// SourceStats is one source's funnel through a single run: how many signals it
// returned, and where they died.
type SourceStats struct {
	Fetched    int // signals returned by this fetcher
	Stale      int // dropped by the freshness gate
	Seen       int // skipped as already processed
	Filtered   int // dropped by the keyword/boolean pre-filter
	Classified int // signals sent to the classifier
	Delivered  int // posted in realtime
	Digest     int // batched (accumulated, not posted this run)
	Dropped    int // noise or below the digest threshold
	Errors     int // fetch/classify/deliver errors (signal retried next run)
}

// Stats summarizes one beacon run: the per-source funnel, plus its column sums.
//
// The split matters because the aggregate cannot answer the first question worth
// asking when a channel goes quiet: is a source returning nothing, or returning
// plenty that is all being filtered out? Those have opposite fixes, and a summed
// "filtered" hides both. (Lobsters was the second case for its whole life so
// far: 25 fetched and 25 filtered every poll, invisible next to HN's numbers.)
type Stats struct {
	SourceStats                        // the run total, summed across sources
	BySource    map[string]SourceStats // per source, keyed by Fetcher.Name()
}

// tally accumulates a run's per-source counters. Only one counter is ever
// incremented per outcome: the totals in Stats are derived from these at the end
// rather than tracked in parallel.
type tally map[string]*SourceStats

// at returns the mutable counters for one source, creating them on first sight.
func (t tally) at(source string) *SourceStats {
	s, ok := t[source]
	if !ok {
		s = &SourceStats{}
		t[source] = s
	}
	return s
}

// rollup snapshots the tally into Stats, summing the columns for the total.
func (t tally) rollup() Stats {
	st := Stats{BySource: make(map[string]SourceStats, len(t))}
	for name, s := range t {
		st.BySource[name] = *s
		st.Fetched += s.Fetched
		st.Stale += s.Stale
		st.Seen += s.Seen
		st.Filtered += s.Filtered
		st.Classified += s.Classified
		st.Delivered += s.Delivered
		st.Digest += s.Digest
		st.Dropped += s.Dropped
		st.Errors += s.Errors
	}
	return st
}

// RunBeacon executes the full pipeline for one beacon.
func RunBeacon(ctx context.Context, cfg config.BeaconConfig, deps Deps, log *slog.Logger) (Stats, error) {
	if deps.Deduper == nil {
		return Stats{}, errors.New("engine: nil deduper")
	}
	if deps.Recorder == nil {
		return Stats{}, errors.New("engine: nil recorder")
	}
	if deps.Classifier == nil {
		return Stats{}, errors.New("engine: nil classifier")
	}
	if deps.Sink == nil {
		return Stats{}, errors.New("engine: nil sink")
	}

	// max_age freshness gate (empty = no gate).
	var maxAge time.Duration
	if cfg.MaxAge != "" {
		d, err := time.ParseDuration(cfg.MaxAge)
		if err != nil {
			return Stats{}, err
		}
		maxAge = d
	}

	bc := core.BeaconContext{
		Beacon:  cfg.Name,
		Context: cfg.Context,
		Buckets: cfg.Buckets,
	}

	counts := tally{}

	tracer := telemetry.Tracer()
	ctx, pollSpan := tracer.Start(ctx, "beacon.poll")
	pollSpan.SetAttributes(
		attribute.String(telemetry.AttrBeacon, cfg.Name),
		attribute.String(telemetry.AttrMode, cfg.Mode),
	)
	defer func() {
		st := counts.rollup()
		pollSpan.SetAttributes(
			attribute.Int(telemetry.AttrFetched, st.Fetched),
			attribute.Int(telemetry.AttrDelivered, st.Delivered),
		)
		// The per-source funnel goes on the span too, so the grid answers which
		// source went quiet, and where in the funnel it died, without a log dive.
		for name, s := range st.BySource {
			pollSpan.SetAttributes(
				attribute.Int(telemetry.SourceAttr(name, "fetched"), s.Fetched),
				attribute.Int(telemetry.SourceAttr(name, "filtered"), s.Filtered),
				attribute.Int(telemetry.SourceAttr(name, "classified"), s.Classified),
				attribute.Int(telemetry.SourceAttr(name, "delivered"), s.Delivered),
			)
		}
		pollSpan.End()
	}()

	// --- fetch (over-collect from every source, sequentially) ---
	var signals []core.Signal
	for i, f := range deps.Fetchers {
		// Spread the fetch phase so several sources do not all fire at once.
		if i > 0 && deps.SourceSpacing > 0 {
			select {
			case <-ctx.Done():
				return counts.rollup(), ctx.Err()
			case <-time.After(deps.SourceSpacing):
			}
		}
		fctx, fspan := tracer.Start(ctx, "source.fetch")
		fspan.SetAttributes(attribute.String(telemetry.AttrSource, f.Name()))
		got, err := f.Fetch(fctx)
		if err != nil {
			counts.at(f.Name()).Errors++
			fspan.RecordError(err)
			fspan.SetStatus(codes.Error, "fetch failed")
			log.Warn("fetch failed", "beacon", cfg.Name, "source", f.Name(), "err", err)
		}
		fspan.SetAttributes(attribute.Int(telemetry.AttrFetched, len(got)))
		fspan.End()
		counts.at(f.Name()).Fetched += len(got)
		signals = append(signals, got...)
	}

	// Sources that already narrowed by keyword server-side skip the redundant
	// client-side pre-filter, whose title-only matching would drop legitimate
	// hits that matched in the body or URL.
	prefiltered := make(map[string]bool)
	for _, f := range deps.Fetchers {
		if kp, ok := f.(core.KeywordPrefilter); ok && kp.PrefiltersByKeyword() {
			prefiltered[f.Name()] = true
		}
	}

	now := time.Now()
	for _, s := range signals {
		src := counts.at(s.Source)

		// --- freshness ---
		if maxAge > 0 && !s.CreatedAt.IsZero() && now.Sub(s.CreatedAt) > maxAge {
			src.Stale++
			continue
		}

		// --- novelty (dedupe, scoped to this beacon) ---
		seen, err := deps.Deduper.Seen(cfg.Name, s.Source, s.ID)
		if err != nil {
			src.Errors++
			log.Warn("seen check failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			continue
		}
		if seen {
			src.Seen++
			continue
		}

		// --- pre-filter (cheap, deterministic) ---
		// Skipped when the source already narrowed to the topic: for the whole
		// source (a keyword search API like HN or Reddit) or for this one signal
		// (a Lobsters tag-scoped feed the operator configured).
		if !prefiltered[s.Source] && !s.Prenarrowed && !filter.Match(s, cfg.Keywords, cfg.Filter) {
			src.Filtered++
			if err := deps.Deduper.MarkSeen(cfg.Name, s.Source, s.ID); err != nil {
				log.Warn("mark seen failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			}
			continue
		}

		// --- classify (LLM) ---
		// One span per turn, created here as "signal.classify". The AI adapter
		// enriches it with GenAI token metadata and renames it to the semconv
		// `chat {model}` form; here we record the pointer fields (source, post
		// id) and, on return, the verdict's bucket/fit/reasoning summary. Never
		// the content.
		src.Classified++
		cctx, cspan := tracer.Start(ctx, "signal.classify")
		cspan.SetAttributes(
			attribute.String(telemetry.AttrSource, s.Source),
			attribute.String(telemetry.AttrPostID, s.ID),
		)
		v, err := deps.Classifier.Classify(cctx, s, bc)
		if err != nil {
			// No MarkSeen: leave it to retry on the next run.
			src.Errors++
			cspan.RecordError(err)
			cspan.SetStatus(codes.Error, "classify failed")
			cspan.End()
			log.Warn("classify failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			continue
		}
		cspan.SetAttributes(
			attribute.String(telemetry.AttrBucket, v.Bucket),
			attribute.Float64(telemetry.AttrFit, v.Fit),
			attribute.String(telemetry.AttrReasoningSummary, v.Reasoning),
		)
		cspan.End()

		// --- decide ---
		if v.Bucket == "noise" || v.Fit < cfg.Thresholds.Digest {
			src.Dropped++
			if err := deps.Deduper.MarkSeen(cfg.Name, s.Source, s.ID); err != nil {
				log.Warn("mark seen failed", "beacon", cfg.Name, "id", s.ID, "err", err)
			}
			continue
		}

		fnd := core.Finding{Beacon: cfg.Name, Signal: s, Verdict: v}

		if v.Fit >= cfg.Thresholds.Realtime {
			dctx, dspan := tracer.Start(ctx, "finding.deliver")
			dspan.SetAttributes(
				attribute.String(telemetry.AttrSource, s.Source),
				attribute.String(telemetry.AttrPostID, s.ID),
				attribute.String(telemetry.AttrBucket, v.Bucket),
				attribute.Float64(telemetry.AttrFit, v.Fit),
			)
			if err := deps.Sink.Deliver(dctx, fnd); err != nil {
				// No Record/MarkSeen: retry delivery next run.
				src.Errors++
				dspan.RecordError(err)
				dspan.SetStatus(codes.Error, "deliver failed")
				dspan.End()
				log.Warn("deliver failed", "beacon", cfg.Name, "id", s.ID, "err", err)
				continue
			}
			dspan.End()
			src.Delivered++
		} else {
			src.Digest++
		}

		// Record marks the signal seen and accumulates it for the digest.
		if err := deps.Recorder.Record(fnd); err != nil {
			src.Errors++
			log.Warn("record failed", "beacon", cfg.Name, "id", s.ID, "err", err)
		}
	}

	return counts.rollup(), nil
}
