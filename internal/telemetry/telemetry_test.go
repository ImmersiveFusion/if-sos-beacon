package telemetry

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAllowed(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"sosbeacon.bucket", true},
		{"sosbeacon.reasoning_summary", true},
		{"gen_ai.usage.input_tokens", true},
		{"gen_ai.response.model", true},
		{"gen_ai.response.finish_reasons", true},
		// Denied: full content and secrets. gen_ai.request.messages is the key
		// case a prefix rule would have leaked; the explicit allowlist drops it.
		{"gen_ai.prompt.0.content", false},
		{"gen_ai.completion.0.content", false},
		{"gen_ai.request.messages", false},
		{"gen_ai.input.messages", false},
		{"http.url", false},
		{"webhook.url", false},
		{"api.key", false},
		{"random.attr", false},
	}
	for _, tc := range tests {
		if got := allowed(tc.key); got != tc.want {
			t.Errorf("allowed(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// capExporter records the attributes of every span it is asked to export.
type capExporter struct {
	byName map[string][]attribute.KeyValue
}

func (c *capExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	if c.byName == nil {
		c.byName = map[string][]attribute.KeyValue{}
	}
	for _, s := range spans {
		c.byName[s.Name()] = s.Attributes()
	}
	return nil
}

func (c *capExporter) Shutdown(context.Context) error { return nil }

func TestAllowlistExporter_ScrubsDisallowed(t *testing.T) {
	stub := tracetest.SpanStub{
		Name: "signal.classify",
		Attributes: []attribute.KeyValue{
			attribute.String("sosbeacon.bucket", "seeker"),
			attribute.Float64("sosbeacon.fit", 0.9),
			attribute.String("sosbeacon.reasoning_summary", "asked for a tool"),
			attribute.Int("gen_ai.usage.input_tokens", 120),
			attribute.String("gen_ai.prompt.0.content", "SECRET FULL PROMPT"),
			attribute.String("gen_ai.completion.0.content", "SECRET COMPLETION"),
			attribute.String("http.url", "https://discord.com/api/webhooks/1/TOKEN_CANARY"),
			attribute.String("api.key", "sk-CANARY"),
		},
	}
	ros := tracetest.SpanStubs{stub}.Snapshots()

	cap := &capExporter{}
	exp := &allowlistExporter{next: cap}
	if err := exp.ExportSpans(context.Background(), ros); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}

	got := map[string]bool{}
	for _, kv := range cap.byName["signal.classify"] {
		got[string(kv.Key)] = true
	}

	for _, want := range []string{"sosbeacon.bucket", "sosbeacon.fit", "sosbeacon.reasoning_summary", "gen_ai.usage.input_tokens"} {
		if !got[want] {
			t.Errorf("allowlisted attr %q was dropped", want)
		}
	}
	for _, deny := range []string{"gen_ai.prompt.0.content", "gen_ai.completion.0.content", "http.url", "api.key"} {
		if got[deny] {
			t.Errorf("disallowed attr %q leaked past the allowlist", deny)
		}
	}
}

func TestInit_NoopWithoutEndpoint(t *testing.T) {
	// No OTEL_EXPORTER_OTLP_* env set: Init must be a clean no-op.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	shutdown, err := Init(context.Background(), nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned a nil shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("noop shutdown returned error: %v", err)
	}
	// Tracer must be usable regardless (no-op tracer).
	_, span := Tracer().Start(context.Background(), "x")
	span.End()
}

// TestNewResource_NoSchemaConflict is the regression test for the bug that left
// every OTLP-configured deployment exporting nothing: resource.Default() carries
// the SDK's own semconv schema URL, and merging a resource stamped with a
// DIFFERENT schema URL returns ErrSchemaURLConflict, which Init used to return,
// leaving the global no-op provider in place. The merge must stay error-free
// across SDK bumps, and service.name must survive it.
func TestNewResource_NoSchemaConflict(t *testing.T) {
	res := newResource()
	if res == nil {
		t.Fatal("newResource returned nil")
	}
	var name string
	for _, kv := range res.Attributes() {
		if string(kv.Key) == "service.name" {
			name = kv.Value.AsString()
		}
	}
	if name != serviceName {
		t.Errorf("service.name = %q, want %q", name, serviceName)
	}
	// The SDK's schema URL must be preserved, not blanked by a conflict.
	if res.SchemaURL() == "" {
		t.Error("merged resource has an empty schema URL (schema conflict blanked it)")
	}
	if res.SchemaURL() != resource.Default().SchemaURL() {
		t.Errorf("schema URL = %q, want the SDK default %q", res.SchemaURL(), resource.Default().SchemaURL())
	}
}

// TestInit_InstallsSDKProviderWithEndpoint asserts the whole point of Init: with
// an OTLP endpoint configured, the installed global provider must be a real SDK
// provider, not the no-op passthrough. The endpoint is unroutable on purpose:
// the gRPC exporter connects lazily, so this needs no network.
func TestInit_InstallsSDKProviderWithEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otlp.invalid.example:443")

	shutdown, err := Init(context.Background(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Init with an endpoint configured must succeed, got: %v", err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(sctx)
	})

	tp := otel.GetTracerProvider()
	if _, ok := tp.(*sdktrace.TracerProvider); !ok {
		t.Fatalf("global tracer provider is %T, want *sdktrace.TracerProvider (no spans are exported otherwise)", tp)
	}
}
