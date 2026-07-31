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
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/ai"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/delivery"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/engine"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/health"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/sources"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/store"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/telemetry"
)

// version is stamped at release time by goreleaser via -ldflags "-X main.version=...".
// Local and dev builds report "dev". It appears in the startup banner so a running
// container announces exactly which image it is.
var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "path to the beacon config file")
	logLevel := flag.String("log-level", "", "log verbosity: debug, info, warn, error (env SOS_BEACON_LOG_LEVEL)")
	interval := flag.Duration("interval", 0, "container loop mode: poll every interval (e.g. 10m); 0 = one-shot. Per-beacon poll_interval overrides this.")
	healthAddr := flag.String("health-addr", ":8080", "container loop mode: address for the /readyz and /healthz HTTP endpoints; empty disables")
	purgeSource := flag.String("purge-source", "", "delete everything the store holds from this source (all beacons), then exit. For a platform that makes deletion a term of access, e.g. -purge-source reddit")
	flag.Parse()

	log := newLogger(resolveLevel(*logLevel))

	// Graceful shutdown: SIGTERM (container stop) and SIGINT (Ctrl-C) cancel ctx.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *purgeSource != "" {
		if err := purge(*configPath, *purgeSource, log); err != nil {
			log.Error("purge failed", "source", *purgeSource, "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(ctx, *configPath, *interval, *healthAddr, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// purge deletes everything the configured store holds from one source and
// exits. It fetches nothing and posts nothing.
//
// This is the operator's answer to a platform that can revoke access and then
// require deletion of what you kept (Reddit's Data API Terms S6 / S3.2 are the
// case that prompted it). The point is that complying is a command with a
// receipt rather than hand-written SQL against a live store on a deadline.
func purge(configPath, source string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Build(cfg.Store)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	n, err := st.PurgeSource(source)
	if err != nil {
		return err
	}
	// Printed, not just logged: this is a receipt an operator may need to show.
	bannerf("purged %d rows from source %q (store=%s)\n", n, source, cfg.Store.Type)
	log.Info("purge complete", "source", source, "rows", n, "store", cfg.Store.Type)
	return nil
}

func run(ctx context.Context, configPath string, globalInterval time.Duration, healthAddr string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	// Startup banner: printed regardless of log level (same pattern as tracegen), so a
	// running container always announces itself even at SOS_BEACON_LOG_LEVEL=error.
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "none"
	}
	bannerf("sos-beacon %s starting: beacons=%d store=%s otlp=%s interval=%s\n",
		version, len(cfg.Beacons), cfg.Store.Type, endpoint, globalInterval)

	// --- OTel tracing (no-op unless an OTLP endpoint is configured via env) ---
	// Logged at ERROR, not WARN: an operator who configured an OTLP endpoint and
	// got no traces must see why at the container's errors-only log level.
	shutdownTracing, err := telemetry.Init(ctx, log)
	if err != nil {
		log.Error("tracing init FAILED; running without traces", "endpoint", endpoint, "err", err)
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
		// Liveness: a hung poll must be restarted, but a beacon legitimately sleeps
		// up to its interval between polls, so the staleness window is 2x the longest
		// effective interval. Startup counts as the first beat (see health.New).
		staleAfter := 2 * maxPollInterval(cfg, globalInterval)
		hm := health.New(staleAfter)
		if healthAddr != "" {
			hm.Serve(healthAddr, func(err error) { log.Error("health server stopped", "err", err) })
			log.Info("health endpoints serving", "addr", healthAddr, "stale_after", staleAfter.String())
		}
		return runLoop(ctx, cfg, classifier, st, globalInterval, hm, log)
	}
	for _, b := range cfg.Beacons {
		runBeacon(ctx, b, classifier, st, 0, log) // one-shot: no source spacing
	}
	return nil
}

// maxPollInterval returns the largest effective poll interval across beacons, used
// to size the liveness staleness window. Falls back to a minute if nothing resolves
// (should not happen in loop mode, where at least one interval is > 0).
func maxPollInterval(cfg *config.Config, globalInterval time.Duration) time.Duration {
	max := globalInterval
	for _, b := range cfg.Beacons {
		if iv, err := resolveInterval(b, globalInterval); err == nil && iv > max {
			max = iv
		}
	}
	if max <= 0 {
		max = time.Minute
	}
	return max
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
func runLoop(ctx context.Context, cfg *config.Config, classifier core.Classifier, st core.Store, globalInterval time.Duration, hm *health.Monitor, log *slog.Logger) error {
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
			loopBeacon(ctx, b, classifier, st, iv, stagger, hm, log)
		}(b, iv, stagger)
	}
	hm.Ready() // startup complete: /readyz now reports 200
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
func loopBeacon(ctx context.Context, b config.BeaconConfig, classifier core.Classifier, st core.Store, interval, stagger time.Duration, hm *health.Monitor, log *slog.Logger) {
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
	hm.Beat() // forward progress: keeps /healthz green

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
			hm.Beat()
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
	// Per-source funnel first: the aggregate below cannot distinguish a source
	// that fetched nothing from one that fetched plenty and had it all filtered,
	// and that distinction is the whole diagnosis when a channel goes quiet.
	//
	// A tally carrying errors is logged at ERROR, not INFO. Containers run at
	// SOS_BEACON_LOG_LEVEL=error, so an INFO-only summary means a beacon whose
	// fetches or classifications are failing every poll looks identical to a
	// healthy quiet one: silence. Severity follows the content, not the phase.
	for _, name := range sortedSources(stats.BySource) {
		s := stats.BySource[name]
		log.Log(ctx, levelFor(s.Errors), "source complete",
			"beacon", b.Name,
			"source", name,
			"fetched", s.Fetched,
			"stale", s.Stale,
			"seen", s.Seen,
			"filtered", s.Filtered,
			"classified", s.Classified,
			"delivered", s.Delivered,
			"digest", s.Digest,
			"dropped", s.Dropped,
			"errors", s.Errors,
		)
	}
	log.Log(ctx, levelFor(stats.Errors), "beacon complete",
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

// levelFor raises a run summary to ERROR when it carries errors, so failures are
// never invisible at the container's errors-only log level.
func levelFor(errCount int) slog.Level {
	if errCount > 0 {
		return slog.LevelError
	}
	return slog.LevelInfo
}

// sortedSources returns the source names in a stable order, so successive poll
// summaries are diffable rather than shuffled by map iteration.
func sortedSources(bySource map[string]engine.SourceStats) []string {
	names := make([]string, 0, len(bySource))
	for n := range bySource {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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

// bannerf writes operator-orientation output to stderr regardless of log level, so a
// running container always announces itself even at SOS_BEACON_LOG_LEVEL=error. It is
// not a leveled log (slog has no "always" severity), and it shares stderr with the slog
// stream so the two stay ordered. tracegen uses the same pattern.
func bannerf(format string, a ...any) { fmt.Fprintf(os.Stderr, format, a...) }
