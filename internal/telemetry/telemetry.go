// Package telemetry wires OpenTelemetry tracing for the beacon and enforces the
// OVI-5 span hygiene gate: only structure and metadata (plus the reasoning
// SUMMARY) leave the process. Full prompt/completion content and every secret
// (webhook URLs, API keys) are scrubbed before export.
//
// Enforcement is twofold: the instrumentation code only ever sets allowlisted
// attributes, and, as defense in depth, a filtering exporter drops any attribute
// that is not on the allowlist before it reaches the OTLP wire.
package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope (tracer name) for beacon spans.
const ScopeName = "github.com/ImmersiveFusion/sos-beacon"

// serviceName is the OTel service.name for the exported resource.
const serviceName = "sos-beacon"

// Custom attribute keys. Everything the beacon puts on a span lives under the
// sosbeacon. namespace (structure/metadata) or the gen_ai. namespace (model
// metadata). The allowlist below keys off these prefixes.
const (
	AttrBeacon           = "sosbeacon.beacon"
	AttrMode             = "sosbeacon.mode"
	AttrSource           = "sosbeacon.source"
	AttrPostID           = "sosbeacon.post_id"
	AttrBucket           = "sosbeacon.bucket"
	AttrFit              = "sosbeacon.fit"
	AttrReasoningSummary = "sosbeacon.reasoning_summary"
	AttrFetched          = "sosbeacon.fetched"
	AttrDelivered        = "sosbeacon.delivered"
)

// SourceAttr builds the per-source funnel attribute key for one field, e.g.
// SourceAttr("lobsters", "filtered") = "sosbeacon.src.lobsters.filtered". Keys
// stay inside the sosbeacon. namespace, so they pass the OVI-5 allowlist and
// carry counts only, never content.
func SourceAttr(source, field string) string {
	return "sosbeacon.src." + source + "." + field
}

// Tracer returns the beacon's tracer. Before Init installs a provider this is a
// no-op tracer, so instrumentation is always safe to call.
func Tracer() trace.Tracer { return otel.Tracer(ScopeName) }

// Init installs a global tracer provider exporting over OTLP/gRPC, with the
// OVI-5 allowlist filter in front of the batch exporter. It is a no-op (nil-safe
// shutdown, no provider installed) unless an OTLP endpoint is configured via the
// standard OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_TRACES_ENDPOINT env
// vars, so local one-shot runs stay quiet unless the operator opts in.
//
// log receives asynchronous export failures (see errorHandler): the batch
// processor exports on a background goroutine, so a rejected api-key or an
// unreachable collector never surfaces as Init's error. A nil log is allowed and
// silences them.
func Init(ctx context.Context, log *slog.Logger) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if !otlpConfigured() {
		return noop, nil
	}

	exp, err := otlptracegrpc.New(ctx) // reads OTEL_EXPORTER_OTLP_* from the environment
	if err != nil {
		return noop, err
	}

	// Export failures are asynchronous and were previously dropped on the floor.
	if log != nil {
		otel.SetErrorHandler(errorHandler{log: log})
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(&allowlistExporter{next: exp}),
		sdktrace.WithResource(newResource()),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// newResource builds the exported resource: the SDK defaults plus service.name.
//
// The service.name attribute is merged SCHEMALESS on purpose. resource.Default()
// carries the schema URL of whatever semconv version the SDK ships, and
// resource.Merge treats two DIFFERENT non-empty schema URLs as a conflict and
// returns an error. Merging a schema-stamped resource here therefore breaks
// every time an SDK bump moves that version, and the old code returned that
// error from Init, which silently disabled tracing altogether (the global no-op
// provider stayed installed and no span ever left the process). A schemaless
// resource has no URL to conflict with, so the merge always succeeds and the
// result keeps the SDK's own schema URL. Merge cannot fail on this input, so
// there is no error to return; the fallback is defense in depth.
func newResource() *resource.Resource {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName),
	))
	if err != nil {
		return resource.Default()
	}
	return res
}

// errorHandler routes the OTel SDK's asynchronous errors (failed exports, a
// rejected api-key, a collector that is not reachable) into the beacon's logger
// at ERROR, so a beacon that thinks it is exporting but is not says so at the
// container's errors-only log level.
type errorHandler struct{ log *slog.Logger }

func (h errorHandler) Handle(err error) {
	h.log.Error("otel export error", "err", err)
}

func otlpConfigured() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// genAIAllowed is the EXPLICIT set of GenAI metadata keys permitted on the wire.
// gen_ai. uses an allowlist, not a prefix rule, on purpose: prefix-allowing the
// namespace would let content-bearing keys (gen_ai.prompt.*, gen_ai.completion.*,
// gen_ai.request.messages, gen_ai.input/output.messages) leak. Only metadata.
var genAIAllowed = map[string]bool{
	"gen_ai.operation.name":          true,
	"gen_ai.system":                  true,
	"gen_ai.request.model":           true,
	"gen_ai.response.model":          true,
	"gen_ai.usage.input_tokens":      true,
	"gen_ai.usage.output_tokens":     true,
	"gen_ai.response.finish_reasons": true,
}

// allowed reports whether an attribute key may be exported (OVI-5). The whole
// sosbeacon. namespace (structure, metadata, reasoning SUMMARY) passes; gen_ai.
// passes only for the explicit metadata keys above. Everything else, including
// full prompt/completion content and any secret, is dropped.
func allowed(key string) bool {
	if strings.HasPrefix(key, "sosbeacon.") {
		return true
	}
	return genAIAllowed[key]
}

// filterAttrs returns only the allowlisted attributes.
func filterAttrs(in []attribute.KeyValue) []attribute.KeyValue {
	out := in[:0:0]
	for _, kv := range in {
		if allowed(string(kv.Key)) {
			out = append(out, kv)
		}
	}
	return out
}

// allowlistExporter wraps a SpanExporter and strips non-allowlisted attributes
// from every span before delegating. This guarantees the OVI-5 gate even if
// instrumentation elsewhere ever sets an attribute it should not.
type allowlistExporter struct {
	next sdktrace.SpanExporter
}

func (e *allowlistExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	filtered := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		filtered[i] = filteredSpan{ReadOnlySpan: s}
	}
	return e.next.ExportSpans(ctx, filtered)
}

func (e *allowlistExporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

// filteredSpan is a ReadOnlySpan view that exposes only allowlisted attributes.
// All other span data (name, timing, status, events, links) passes through.
type filteredSpan struct {
	sdktrace.ReadOnlySpan
}

func (f filteredSpan) Attributes() []attribute.KeyValue {
	return filterAttrs(f.ReadOnlySpan.Attributes())
}
