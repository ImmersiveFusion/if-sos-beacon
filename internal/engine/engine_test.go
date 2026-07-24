package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/config"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

// --- fake ports ---

type fakeFetcher struct {
	name    string
	signals []core.Signal
	err     error
}

func (f *fakeFetcher) Name() string { return f.name }
func (f *fakeFetcher) Fetch(context.Context) ([]core.Signal, error) {
	return f.signals, f.err
}

type fakeClassifier struct {
	verdict core.Verdict
	err     error
	calls   int
}

func (c *fakeClassifier) Classify(context.Context, core.Signal, core.BeaconContext) (core.Verdict, error) {
	c.calls++
	return c.verdict, c.err
}

type fakeSink struct {
	delivered []core.Finding
	err       error
}

func (s *fakeSink) Deliver(_ context.Context, f core.Finding) error {
	if s.err != nil {
		return s.err
	}
	s.delivered = append(s.delivered, f)
	return nil
}

type fakeStore struct {
	seen      map[string]bool
	marked    []string
	recorded  []core.Finding
	seenErr   error
	markErr   error
	recordErr error
}

func newFakeStore() *fakeStore { return &fakeStore{seen: map[string]bool{}} }

func (s *fakeStore) key(beacon, source, id string) string { return beacon + ":" + source + ":" + id }
func (s *fakeStore) Seen(beacon, source, id string) (bool, error) {
	return s.seen[s.key(beacon, source, id)], s.seenErr
}

func (s *fakeStore) MarkSeen(beacon, source, id string) error {
	s.marked = append(s.marked, s.key(beacon, source, id))
	s.seen[s.key(beacon, source, id)] = true
	return s.markErr
}

func (s *fakeStore) Record(f core.Finding) error {
	s.recorded = append(s.recorded, f)
	s.seen[s.key(f.Beacon, f.Signal.Source, f.Signal.ID)] = true
	return s.recordErr
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func passingSignal() core.Signal {
	return core.Signal{Source: "hn", ID: "1", Title: "datadog bill", CreatedAt: time.Now()}
}

func baseConfig() config.BeaconConfig {
	return config.BeaconConfig{
		Name:       "t",
		Thresholds: config.Thresholds{Realtime: 0.75, Digest: 0.50},
	}
}

// --- decision routing (table-driven) ---

func TestRunBeacon_DecisionRouting(t *testing.T) {
	tests := []struct {
		name          string
		verdict       core.Verdict
		deliverErr    error
		wantDelivered int
		wantDigest    int
		wantDropped   int
		wantErrors    int
		wantRecorded  int
		wantMarked    int
	}{
		{
			name:          "realtime deliver",
			verdict:       core.Verdict{Bucket: "seeker", Fit: 0.9},
			wantDelivered: 1, wantRecorded: 1,
		},
		{
			name:         "digest batch",
			verdict:      core.Verdict{Bucket: "seeker", Fit: 0.6},
			wantDigest:   1,
			wantRecorded: 1,
		},
		{
			name:        "drop below digest",
			verdict:     core.Verdict{Bucket: "seeker", Fit: 0.2},
			wantDropped: 1, wantMarked: 1,
		},
		{
			name:        "drop noise bucket",
			verdict:     core.Verdict{Bucket: "noise", Fit: 0.99},
			wantDropped: 1, wantMarked: 1,
		},
		{
			name:          "deliver error retries (no record, no mark)",
			verdict:       core.Verdict{Bucket: "seeker", Fit: 0.9},
			deliverErr:    errors.New("boom"),
			wantErrors:    1,
			wantDelivered: 0, wantRecorded: 0, wantMarked: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			sink := &fakeSink{err: tc.deliverErr}
			deps := Deps{
				Fetchers:   []core.Fetcher{&fakeFetcher{name: "hn", signals: []core.Signal{passingSignal()}}},
				Classifier: &fakeClassifier{verdict: tc.verdict},
				Sink:       sink,
				Deduper:    store,
				Recorder:   store,
			}
			st, err := RunBeacon(context.Background(), baseConfig(), deps, discardLogger())
			if err != nil {
				t.Fatalf("RunBeacon: %v", err)
			}
			if st.Delivered != tc.wantDelivered {
				t.Errorf("Delivered = %d, want %d", st.Delivered, tc.wantDelivered)
			}
			if st.Digest != tc.wantDigest {
				t.Errorf("Digest = %d, want %d", st.Digest, tc.wantDigest)
			}
			if st.Dropped != tc.wantDropped {
				t.Errorf("Dropped = %d, want %d", st.Dropped, tc.wantDropped)
			}
			if st.Errors != tc.wantErrors {
				t.Errorf("Errors = %d, want %d", st.Errors, tc.wantErrors)
			}
			if len(store.recorded) != tc.wantRecorded {
				t.Errorf("recorded = %d, want %d", len(store.recorded), tc.wantRecorded)
			}
			if len(store.marked) != tc.wantMarked {
				t.Errorf("marked = %d, want %d", len(store.marked), tc.wantMarked)
			}
			if len(sink.delivered) != tc.wantDelivered {
				t.Errorf("sink delivered = %d, want %d", len(sink.delivered), tc.wantDelivered)
			}
		})
	}
}

// --- gating stages ---

func TestRunBeacon_Gating(t *testing.T) {
	t.Run("already seen skips classify", func(t *testing.T) {
		store := newFakeStore()
		store.seen["t:hn:1"] = true // key is beacon:source:id
		clf := &fakeClassifier{verdict: core.Verdict{Bucket: "seeker", Fit: 0.9}}
		deps := Deps{
			Fetchers:   []core.Fetcher{&fakeFetcher{name: "hn", signals: []core.Signal{passingSignal()}}},
			Classifier: clf, Sink: &fakeSink{}, Deduper: store, Recorder: store,
		}
		st, _ := RunBeacon(context.Background(), baseConfig(), deps, discardLogger())
		if st.Seen != 1 || clf.calls != 0 {
			t.Errorf("Seen=%d classifier.calls=%d, want 1 and 0", st.Seen, clf.calls)
		}
	})

	t.Run("filter miss marks seen and skips classify", func(t *testing.T) {
		store := newFakeStore()
		clf := &fakeClassifier{verdict: core.Verdict{Bucket: "seeker", Fit: 0.9}}
		cfg := baseConfig()
		cfg.Keywords = []string{"kubernetes"} // signal title has no such word
		deps := Deps{
			Fetchers:   []core.Fetcher{&fakeFetcher{name: "hn", signals: []core.Signal{passingSignal()}}},
			Classifier: clf, Sink: &fakeSink{}, Deduper: store, Recorder: store,
		}
		st, _ := RunBeacon(context.Background(), cfg, deps, discardLogger())
		if st.Filtered != 1 || clf.calls != 0 || len(store.marked) != 1 {
			t.Errorf("Filtered=%d calls=%d marked=%d, want 1/0/1", st.Filtered, clf.calls, len(store.marked))
		}
	})

	t.Run("freshness drops stale", func(t *testing.T) {
		store := newFakeStore()
		clf := &fakeClassifier{verdict: core.Verdict{Bucket: "seeker", Fit: 0.9}}
		sig := passingSignal()
		sig.CreatedAt = time.Now().Add(-48 * time.Hour)
		cfg := baseConfig()
		cfg.MaxAge = "12h"
		deps := Deps{
			Fetchers:   []core.Fetcher{&fakeFetcher{name: "hn", signals: []core.Signal{sig}}},
			Classifier: clf, Sink: &fakeSink{}, Deduper: store, Recorder: store,
		}
		st, _ := RunBeacon(context.Background(), cfg, deps, discardLogger())
		if st.Stale != 1 || clf.calls != 0 {
			t.Errorf("Stale=%d calls=%d, want 1/0", st.Stale, clf.calls)
		}
	})

	t.Run("classify error does not mark seen (retries next run)", func(t *testing.T) {
		store := newFakeStore()
		clf := &fakeClassifier{err: errors.New("llm down")}
		deps := Deps{
			Fetchers:   []core.Fetcher{&fakeFetcher{name: "hn", signals: []core.Signal{passingSignal()}}},
			Classifier: clf, Sink: &fakeSink{}, Deduper: store, Recorder: store,
		}
		st, _ := RunBeacon(context.Background(), baseConfig(), deps, discardLogger())
		if st.Errors != 1 || len(store.marked) != 0 || len(store.recorded) != 0 {
			t.Errorf("Errors=%d marked=%d recorded=%d, want 1/0/0", st.Errors, len(store.marked), len(store.recorded))
		}
	})
}

// prefilteredFetcher is a source that declares it already narrowed by keyword.
type prefilteredFetcher struct {
	name    string
	signals []core.Signal
}

func (f *prefilteredFetcher) Name() string                                 { return f.name }
func (f *prefilteredFetcher) Fetch(context.Context) ([]core.Signal, error) { return f.signals, nil }
func (f *prefilteredFetcher) PrefiltersByKeyword() bool                    { return true }

func TestRunBeacon_KeywordPrefilterSkipsFilter(t *testing.T) {
	store := newFakeStore()
	clf := &fakeClassifier{verdict: core.Verdict{Bucket: "seeker", Fit: 0.9}}
	cfg := baseConfig()
	cfg.Keywords = []string{"kubernetes"} // the signal's title lacks this word

	// The signal would fail the keyword filter, but its source declares it was
	// already keyword-queried, so the engine must classify it anyway.
	sig := core.Signal{Source: "hn", ID: "9", Title: "a story with no kw", CreatedAt: time.Now()}
	deps := Deps{
		Fetchers:   []core.Fetcher{&prefilteredFetcher{name: "hn", signals: []core.Signal{sig}}},
		Classifier: clf, Sink: &fakeSink{}, Deduper: store, Recorder: store,
	}
	st, _ := RunBeacon(context.Background(), cfg, deps, discardLogger())
	if st.Filtered != 0 || clf.calls != 1 || st.Classified != 1 {
		t.Errorf("Filtered=%d calls=%d classified=%d, want 0/1/1 (prefiltered source bypasses filter)", st.Filtered, clf.calls, st.Classified)
	}
}

func TestRunBeacon_DedupeIsPerBeacon(t *testing.T) {
	// One shared store, two beacons, the same (source, id). Beacon A marks it
	// seen (filtered); beacon B must still see it as novel and classify it.
	store := newFakeStore()
	sig := core.Signal{Source: "lobsters", ID: "shared", Title: "off-topic for A", CreatedAt: time.Now()}

	cfgA := baseConfig()
	cfgA.Name = "beacon-a"
	cfgA.Keywords = []string{"datadog"} // signal lacks it -> A filters + marks seen
	depsA := Deps{
		Fetchers:   []core.Fetcher{&fakeFetcher{name: "lobsters", signals: []core.Signal{sig}}},
		Classifier: &fakeClassifier{}, Sink: &fakeSink{}, Deduper: store, Recorder: store,
	}
	if _, err := RunBeacon(context.Background(), cfgA, depsA, discardLogger()); err != nil {
		t.Fatalf("beacon A: %v", err)
	}

	cfgB := baseConfig()
	cfgB.Name = "beacon-b" // no keywords -> filter open, should classify
	clfB := &fakeClassifier{verdict: core.Verdict{Bucket: "seeker", Fit: 0.9}}
	depsB := Deps{
		Fetchers:   []core.Fetcher{&fakeFetcher{name: "lobsters", signals: []core.Signal{sig}}},
		Classifier: clfB, Sink: &fakeSink{}, Deduper: store, Recorder: store,
	}
	stB, err := RunBeacon(context.Background(), cfgB, depsB, discardLogger())
	if err != nil {
		t.Fatalf("beacon B: %v", err)
	}
	if stB.Seen != 0 || clfB.calls != 1 {
		t.Errorf("beacon B Seen=%d calls=%d, want 0/1: A's mark must not starve B", stB.Seen, clfB.calls)
	}
}

func TestRunBeacon_NilDepsRejected(t *testing.T) {
	_, err := RunBeacon(context.Background(), baseConfig(), Deps{}, discardLogger())
	if err == nil {
		t.Fatal("expected error for empty Deps")
	}
}

// --- source spacing ---

type timedFetcher struct {
	name string
	mu   *sync.Mutex
	at   *[]time.Time
}

func (f *timedFetcher) Name() string { return f.name }
func (f *timedFetcher) Fetch(context.Context) ([]core.Signal, error) {
	f.mu.Lock()
	*f.at = append(*f.at, time.Now())
	f.mu.Unlock()
	return nil, nil
}

func TestRunBeacon_SourceSpacing(t *testing.T) {
	var mu sync.Mutex
	var at []time.Time
	store := newFakeStore()
	const spacing = 30 * time.Millisecond
	deps := Deps{
		Fetchers: []core.Fetcher{
			&timedFetcher{name: "a", mu: &mu, at: &at},
			&timedFetcher{name: "b", mu: &mu, at: &at},
		},
		Classifier:    &fakeClassifier{},
		Sink:          &fakeSink{},
		Deduper:       store,
		Recorder:      store,
		SourceSpacing: spacing,
	}
	if _, err := RunBeacon(context.Background(), baseConfig(), deps, discardLogger()); err != nil {
		t.Fatalf("RunBeacon: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(at) != 2 {
		t.Fatalf("got %d fetches, want 2", len(at))
	}
	if gap := at[1].Sub(at[0]); gap < spacing-5*time.Millisecond {
		t.Errorf("sources fetched %v apart, want >= ~%v (spaced, not bursted)", gap, spacing)
	}
}

// --- OTel span tree ---

func TestRunBeacon_EmitsSpanTree(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	store := newFakeStore()
	deps := Deps{
		Fetchers:   []core.Fetcher{&fakeFetcher{name: "hn", signals: []core.Signal{passingSignal()}}},
		Classifier: &fakeClassifier{verdict: core.Verdict{Bucket: "seeker", Fit: 0.9, Reasoning: "asked for a tool"}},
		Sink:       &fakeSink{},
		Deduper:    store,
		Recorder:   store,
	}
	if _, err := RunBeacon(context.Background(), baseConfig(), deps, discardLogger()); err != nil {
		t.Fatalf("RunBeacon: %v", err)
	}

	names := map[string]bool{}
	for _, s := range exp.GetSpans() {
		names[s.Name] = true
	}
	for _, want := range []string{"beacon.poll", "source.fetch", "signal.classify", "finding.deliver"} {
		if !names[want] {
			t.Errorf("missing span %q; got %v", want, names)
		}
	}
}

// --- anti-slop structural invariant ---

// TestAntiSlop_ModelTextOnlyReachesSink asserts the load-bearing invariant: the
// classifier's prose (Summary/Reasoning) leaves the engine ONLY as a Finding
// handed to the Sink. There is no reply path.
func TestAntiSlop_ModelTextOnlyReachesSink(t *testing.T) {
	const marker = "MODEL_TEXT_CANARY"
	store := newFakeStore()
	sink := &fakeSink{}
	deps := Deps{
		Fetchers:   []core.Fetcher{&fakeFetcher{name: "hn", signals: []core.Signal{passingSignal()}}},
		Classifier: &fakeClassifier{verdict: core.Verdict{Bucket: "seeker", Fit: 0.9, Summary: marker, Reasoning: marker}},
		Sink:       sink,
		Deduper:    store,
		Recorder:   store,
	}
	if _, err := RunBeacon(context.Background(), baseConfig(), deps, discardLogger()); err != nil {
		t.Fatalf("RunBeacon: %v", err)
	}

	// The model text reached the Sink, inside a Finding.
	if len(sink.delivered) != 1 || sink.delivered[0].Verdict.Summary != marker {
		t.Fatalf("model text did not reach the Sink as a Finding: %+v", sink.delivered)
	}

	// Structural: the Sink's sole method consumes a Finding, never a bare reply
	// string. If someone adds a `Deliver(text string)`-style path, this breaks.
	sinkT := reflect.TypeOf((*core.Sink)(nil)).Elem()
	if sinkT.NumMethod() != 1 {
		t.Fatalf("core.Sink has %d methods, want exactly 1 (Deliver)", sinkT.NumMethod())
	}
	deliver, _ := sinkT.MethodByName("Deliver")
	findingT := reflect.TypeOf(core.Finding{})
	foundFinding := false
	for i := 0; i < deliver.Type.NumIn(); i++ {
		if deliver.Type.In(i) == findingT {
			foundFinding = true
		}
		if deliver.Type.In(i).Kind() == reflect.String {
			t.Errorf("Sink.Deliver takes a string arg; model text must travel as a Finding, not raw prose")
		}
	}
	if !foundFinding {
		t.Error("Sink.Deliver must take a core.Finding")
	}

	// Structural: the persistence roles the engine holds cannot post. Recorder
	// takes a Finding (data), Deduper takes only (source, id) identifiers.
	recordT := reflect.TypeOf((*core.Recorder)(nil)).Elem()
	rec, _ := recordT.MethodByName("Record")
	if rec.Type.In(0) != findingT {
		t.Error("Recorder.Record must take a core.Finding, not prose")
	}
}
