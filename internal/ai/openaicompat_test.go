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
