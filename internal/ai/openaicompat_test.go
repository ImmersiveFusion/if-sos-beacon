package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

func sampleBeaconContext() core.BeaconContext {
	return core.BeaconContext{
		Beacon:  "t",
		Context: "topic context",
		Buckets: []core.Bucket{{Name: "seeker", Emoji: "🙋", Definition: "asking"}},
	}
}

func TestClassify_RequestShapeAndParse(t *testing.T) {
	var mu sync.Mutex
	var captured struct {
		Model          string `json:"model"`
		Temperature    float64
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want Bearer sk-test", got)
		}
		if r.Header.Get("api-key") != "" {
			t.Error("api-key header must be absent without apiVersion")
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %q, want .../chat/completions", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		err := json.Unmarshal(body, &captured)
		mu.Unlock()
		if err != nil {
			t.Errorf("decode request: %v", err)
		}
		resp := `{"choices":[{"message":{"content":"{\"bucket\":\"seeker\",\"scores\":{\"seeker\":0.9},\"fit\":0.88,\"summary\":\"s\",\"reasoning\":\"r\"}"}}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	c := NewOpenAICompat(srv.URL, "gpt-x", "sk-test", "")
	v, err := c.Classify(context.Background(), core.Signal{Title: "hi", Source: "hn"}, sampleBeaconContext())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if captured.Temperature != 0 {
		t.Errorf("temperature = %v, want 0", captured.Temperature)
	}
	if captured.ResponseFormat.Type != "json_object" {
		t.Errorf("response_format.type = %q, want json_object", captured.ResponseFormat.Type)
	}
	if len(captured.Messages) != 2 || captured.Messages[0].Role != "system" || captured.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want [system,user]", captured.Messages)
	}
	if !strings.Contains(captured.Messages[0].Content, "topic context") {
		t.Error("system prompt should embed the beacon context")
	}
	if v.Bucket != "seeker" || v.Fit != 0.88 {
		t.Errorf("verdict = %+v, want bucket seeker fit 0.88", v)
	}
}

func TestClassify_AzureAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("api-key"); got != "azkey" {
			t.Errorf("api-key = %q, want azkey", got)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization must be absent when apiVersion is set")
		}
		if got := r.URL.Query().Get("api-version"); got != "2024-10-21" {
			t.Errorf("api-version = %q, want 2024-10-21", got)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"bucket\":\"noise\",\"scores\":{},\"fit\":0.1,\"summary\":\"\",\"reasoning\":\"\"}"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompat(srv.URL, "dep", "azkey", "2024-10-21")
	if _, err := c.Classify(context.Background(), core.Signal{Title: "x"}, sampleBeaconContext()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
}

func TestClassify_RecordsGenAIUsageOnSpan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"model": "gpt-x-2026",
			"usage": {"prompt_tokens": 123, "completion_tokens": 45, "total_tokens": 168},
			"choices": [{"finish_reason": "stop", "message": {"content": "{\"bucket\":\"seeker\",\"scores\":{},\"fit\":0.7,\"summary\":\"s\",\"reasoning\":\"r\"}"}}]
		}`))
	}))
	defer srv.Close()

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	ctx, span := tp.Tracer("test").Start(context.Background(), "signal.classify")

	c := NewOpenAICompat(srv.URL, "gpt-x", "sk-test", "")
	if _, err := c.Classify(ctx, core.Signal{Title: "hi"}, sampleBeaconContext()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	// The adapter renames the turn span to the semconv `chat {model}` form.
	if spans[0].Name != "chat gpt-x" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "chat gpt-x")
	}
	attrs := map[string]attribute.Value{}
	for _, kv := range spans[0].Attributes {
		attrs[string(kv.Key)] = kv.Value
	}
	if v, ok := attrs["gen_ai.usage.input_tokens"]; !ok || v.AsInt64() != 123 {
		t.Errorf("gen_ai.usage.input_tokens = %v (present=%v), want 123", v.AsInt64(), ok)
	}
	if v, ok := attrs["gen_ai.usage.output_tokens"]; !ok || v.AsInt64() != 45 {
		t.Errorf("gen_ai.usage.output_tokens = %v (present=%v), want 45", v.AsInt64(), ok)
	}
	if v, ok := attrs["gen_ai.response.model"]; !ok || v.AsString() != "gpt-x-2026" {
		t.Errorf("gen_ai.response.model = %q (present=%v), want gpt-x-2026", v.AsString(), ok)
	}
	if v, ok := attrs["gen_ai.request.model"]; !ok || v.AsString() != "gpt-x" {
		t.Errorf("gen_ai.request.model = %q (present=%v), want gpt-x", v.AsString(), ok)
	}
	if _, ok := attrs["gen_ai.response.finish_reasons"]; !ok {
		t.Error("gen_ai.response.finish_reasons missing")
	}
}

func TestClassify_BadJSONContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompat(srv.URL, "m", "k", "")
	if _, err := c.Classify(context.Background(), core.Signal{Title: "x"}, sampleBeaconContext()); err == nil {
		t.Fatal("expected parse error for non-JSON model content")
	}
}
