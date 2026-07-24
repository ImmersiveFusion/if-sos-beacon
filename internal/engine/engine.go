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

	tracer := telemetry.Tracer()
	ctx, pollSpan := tracer.Start(ctx, "beacon.poll")
	pollSpan.SetAttributes(
		attribute.String(telemetry.AttrBeacon, cfg.Name),
		attribute.String(telemetry.AttrMode, cfg.Mode),
	)
	defer func() {
		pollSpan.SetAttributes(
			attribute.Int(telemetry.AttrFetched, st.Fetched),
			attribute.Int(telemetry.AttrDelivered, st.Delivered),
		)
		pollSpan.End()
	}()

	// --- fetch (over-collect from every source, sequentially) ---
	var signals []core.Signal
	for i, f := range deps.Fetchers {
		// Spread the fetch phase so several sources do not all fire at once.
		if i > 0 && deps.SourceSpacing > 0 {
			select {
			case <-ctx.Done():
				return st, ctx.Err()
			case <-time.After(deps.SourceSpacing):
			}
		}
		fctx, fspan := tracer.Start(ctx, "source.fetch")
		fspan.SetAttributes(attribute.String(telemetry.AttrSource, f.Name()))
		got, err := f.Fetch(fctx)
		if err != nil {
			st.Errors++
			fspan.RecordError(err)
			fspan.SetStatus(codes.Error, "fetch failed")
			log.Warn("fetch failed", "beacon", cfg.Name, "source", f.Name(), "err", err)
		}
		fspan.SetAttributes(attribute.Int(telemetry.AttrFetched, len(got)))
		fspan.End()
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
		// One span per turn, created here as "signal.classify". The AI adapter
		// enriches it with GenAI token metadata and renames it to the semconv
		// `chat {model}` form; here we record the pointer fields (source, post
		// id) and, on return, the verdict's bucket/fit/reasoning summary. Never
		// the content.
		st.Classified++
		cctx, cspan := tracer.Start(ctx, "signal.classify")
		cspan.SetAttributes(
			attribute.String(telemetry.AttrSource, s.Source),
			attribute.String(telemetry.AttrPostID, s.ID),
		)
		v, err := deps.Classifier.Classify(cctx, s, bc)
		if err != nil {
			// No MarkSeen: leave it to retry on the next run.
			st.Errors++
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
			st.Dropped++
			if err := deps.Deduper.MarkSeen(s.Source, s.ID); err != nil {
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
				st.Errors++
				dspan.RecordError(err)
				dspan.SetStatus(codes.Error, "deliver failed")
				dspan.End()
				log.Warn("deliver failed", "beacon", cfg.Name, "id", s.ID, "err", err)
				continue
			}
			dspan.End()
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
