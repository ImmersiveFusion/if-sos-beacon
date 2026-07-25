// Command sos-beacon fetches public signals, classifies them, and posts pointers
// (never replies) to a destination. It runs in two modes from one binary: a
// one-shot pass over every beacon (the default, for cron / GitHub Actions), or a
// long-running container loop where each beacon polls on its own interval until
// SIGTERM. Every port is wired through its package registry, so adding an adapter
// never touches this file.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/ai"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/delivery"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/engine"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/sources"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/store"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/telemetry"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the beacon config file")
	logLevel := flag.String("log-level", "", "log verbosity: debug, info, warn, error (env SOS_BEACON_LOG_LEVEL)")
	interval := flag.Duration("interval", 0, "container loop mode: poll every interval (e.g. 10m); 0 = one-shot. Per-beacon poll_interval overrides this.")
	flag.Parse()

	log := newLogger(resolveLevel(*logLevel))

	// Graceful shutdown: SIGTERM (container stop) and SIGINT (Ctrl-C) cancel ctx.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *configPath, *interval, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string, globalInterval time.Duration, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	// --- OTel tracing (no-op unless an OTLP endpoint is configured via env) ---
	shutdownTracing, err := telemetry.Init(ctx)
	if err != nil {
		log.Warn("tracing init failed; continuing without it", "err", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(sctx)
	}()

	// --- Persistence port (registry: file | azuresql) ---
	st, err := store.Build(cfg.Store)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// --- AI port (registry: openai-compatible) ---
	if os.Getenv(cfg.AI.KeyEnv) == "" {
		log.Warn("AI key env is empty; classification will fail", "env", cfg.AI.KeyEnv)
	}
	classifier, err := ai.Build(cfg.AI)
	if err != nil {
		return err
	}

	if loopMode(cfg, globalInterval) {
		return runLoop(ctx, cfg, classifier, st, globalInterval, log)
	}
	for _, b := range cfg.Beacons {
		runBeacon(ctx, b, classifier, st, 0, log) // one-shot: no source spacing
	}
	return nil
}

// loopMode is true when any interval is configured (the global -interval flag or
// any per-beacon poll_interval). Otherwise the run is one-shot.
func loopMode(cfg *config.Config, globalInterval time.Duration) bool {
	if globalInterval > 0 {
		return true
	}
	for _, b := range cfg.Beacons {
		if strings.TrimSpace(b.PollInterval) != "" {
			return true
		}
	}
	return false
}

// resolveInterval returns a beacon's effective poll interval: its own
// poll_interval if set, otherwise the global default.
func resolveInterval(b config.BeaconConfig, globalInterval time.Duration) (time.Duration, error) {
	if s := strings.TrimSpace(b.PollInterval); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, err
		}
		return d, nil
	}
	return globalInterval, nil
}

// loopSourceSpacing is the pause between a beacon's source fetches in loop mode,
// so several sources do not fetch in one burst. Small enough not to dent
// coverage latency; one-shot runs fetch back-to-back (no spacing).
const loopSourceSpacing = 1 * time.Second

// runLoop polls every beacon on its own interval until ctx is canceled, then
// waits for in-flight polls to finish before returning. Beacons are phase-
// staggered so equal-interval beacons do not poll in lockstep (anti-burst).
func runLoop(ctx context.Context, cfg *config.Config, classifier core.Classifier, st core.Store, globalInterval time.Duration, log *slog.Logger) error {
	var wg sync.WaitGroup
	n := len(cfg.Beacons)
	for i, b := range cfg.Beacons {
		iv, err := resolveInterval(b, globalInterval)
		if err != nil {
			log.Error("bad poll_interval; skipping beacon", "beacon", b.Name, "value", b.PollInterval, "err", err)
			continue
		}
		// Coverage guard: an interval at or beyond max_age can let posts appear
		// and age past the freshness gate between polls, an unseen time gap.
		if s := strings.TrimSpace(b.MaxAge); s != "" {
			if maxAge, perr := time.ParseDuration(s); perr == nil && coverageGap(iv, maxAge) {
				log.Warn("poll interval >= max_age; posts may age out between polls (coverage gap)",
					"beacon", b.Name, "interval", iv.String(), "max_age", b.MaxAge)
			}
		}
		stagger := staggerOffset(i, n, iv)
		wg.Add(1)
		go func(b config.BeaconConfig, iv, stagger time.Duration) {
			defer wg.Done()
			loopBeacon(ctx, b, classifier, st, iv, stagger, log)
		}(b, iv, stagger)
	}
	log.Info("container loop mode; polling until SIGTERM")
	wg.Wait()
	log.Info("all beacon loops stopped")
	return nil
}

// staggerOffset spreads count beacons evenly across one interval: beacon index
// starts at index/count of its interval. Deterministic (no randomness), so the
// spread is identical across restarts and equal-interval beacons stay separated.
func staggerOffset(index, count int, interval time.Duration) time.Duration {
	if count <= 1 || interval <= 0 {
		return 0
	}
	return time.Duration(int64(interval) * int64(index) / int64(count))
}

// coverageGap reports whether a poll interval is too coarse for a freshness
// window: at interval >= max_age a post can be created and age past the gate
// between two polls, so it is never classified.
func coverageGap(interval, maxAge time.Duration) bool {
	return interval > 0 && maxAge > 0 && interval >= maxAge
}

// loopBeacon runs one beacon immediately, then every interval, until ctx is
// canceled. Each poll uses a background context so a shutdown signal mid-poll
// lets the current poll finish gracefully (bounded by the adapters' own HTTP
// timeouts) rather than tearing it in half.
func loopBeacon(ctx context.Context, b config.BeaconConfig, classifier core.Classifier, st core.Store, interval, stagger time.Duration, log *slog.Logger) {
	// Phase offset: delay this beacon's first poll so beacons do not all fire at
	// startup and equal-interval beacons stay spread across the window.
	if stagger > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(stagger):
		}
	}
	if ctx.Err() != nil {
		return
	}
	runBeacon(context.Background(), b, classifier, st, loopSourceSpacing, log)

	if interval <= 0 {
		return // no interval for this beacon: a single poll in loop mode
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("beacon loop stopping", "beacon", b.Name)
			return
		case <-t.C:
			runBeacon(context.Background(), b, classifier, st, loopSourceSpacing, log)
		}
	}
}

// runBeacon wires the per-beacon ports and runs the pipeline once. Adapter
// problems (an unknown source, a missing webhook) are logged and skipped so one
// misconfigured beacon never sinks the rest of the run.
func runBeacon(ctx context.Context, b config.BeaconConfig, classifier core.Classifier, st core.Store, sourceSpacing time.Duration, log *slog.Logger) {
	// --- Sources port (registry) ---
	var fetchers []core.Fetcher
	for _, src := range b.Sources {
		f, err := sources.Build(src, b)
		if err != nil {
			log.Warn("skipping source", "beacon", b.Name, "source", src, "err", err)
			continue
		}
		fetchers = append(fetchers, f)
	}
	if len(fetchers) == 0 {
		log.Warn("beacon has no usable sources; skipping", "beacon", b.Name)
		return
	}

	// --- Delivery port (registry) ---
	sink, err := delivery.Build(b.Destination)
	if err != nil {
		log.Warn("skipping beacon: no usable destination", "beacon", b.Name, "err", err)
		return
	}

	deps := engine.Deps{
		Fetchers:      fetchers,
		Classifier:    classifier,
		Sink:          sink,
		Deduper:       st,
		Recorder:      st,
		SourceSpacing: sourceSpacing,
	}
	stats, err := engine.RunBeacon(ctx, b, deps, log)
	if err != nil {
		log.Error("beacon failed", "beacon", b.Name, "err", err)
		return
	}
	log.Info("beacon complete",
		"beacon", b.Name,
		"fetched", stats.Fetched,
		"stale", stats.Stale,
		"seen", stats.Seen,
		"filtered", stats.Filtered,
		"classified", stats.Classified,
		"delivered", stats.Delivered,
		"digest", stats.Digest,
		"dropped", stats.Dropped,
		"errors", stats.Errors,
	)
}

// resolveLevel picks the log level: -log-level flag, then SOS_BEACON_LOG_LEVEL,
// then info.
func resolveLevel(flagVal string) slog.Level {
	v := flagVal
	if v == "" {
		v = os.Getenv("SOS_BEACON_LOG_LEVEL")
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
