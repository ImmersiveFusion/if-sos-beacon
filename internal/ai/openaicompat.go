// Package ai holds the AI-port adapters (Classifier implementations). The
// default `openai-compatible` adapter speaks the OpenAI /chat/completions shape,
// which covers Azure OpenAI, OpenAI, Ollama, vLLM, OpenRouter and Groq via
// base_url + key + model. The only non-stdlib dependency is the OpenTelemetry
// trace API, used to record GenAI token metadata on the active classify span.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
	"github.com/ImmersiveFusion/if-sos-beacon/internal/prompts"
)

const defaultBaseURL = "https://api.openai.com/v1"

// OpenAICompat is a Classifier backed by any OpenAI-compatible chat endpoint.
type OpenAICompat struct {
	baseURL    string
	model      string
	key        string
	apiVersion string // set for Azure OpenAI: use api-key header + ?api-version=
	client     *http.Client
}

// NewOpenAICompat builds the adapter. An empty baseURL defaults to the public
// OpenAI API. When apiVersion is set (Azure OpenAI), the request appends
// ?api-version= and authenticates with the api-key header instead of Bearer.
func NewOpenAICompat(baseURL, model, key, apiVersion string) *OpenAICompat {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &OpenAICompat{
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		key:        key,
		apiVersion: apiVersion,
		client:     &http.Client{Timeout: 60 * time.Second},
	}
}

// chatRequest is the subset of the /chat/completions request we send.
type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature"`
	ResponseFormat responseFormat `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// userPayload is the compact JSON the model classifies. Only surfaced fields.
type userPayload struct {
	Title       string   `json:"title"`
	Body        string   `json:"body,omitempty"`
	TopComments []string `json:"top_comments,omitempty"`
	Source      string   `json:"source"`
}

// Classify sends the signal to the model and parses the JSON Verdict back.
func (c *OpenAICompat) Classify(ctx context.Context, s core.Signal, bc core.BeaconContext) (core.Verdict, error) {
	system := prompts.BuildSystem(bc)

	up := userPayload{
		Title:       s.Title,
		Body:        s.Body,
		TopComments: s.TopComments,
		Source:      s.Source,
	}
	userJSON, err := json.Marshal(up)
	if err != nil {
		return core.Verdict{}, fmt.Errorf("marshal user payload: %w", err)
	}

	reqBody := chatRequest{
		Model:          c.model,
		Temperature:    0,
		ResponseFormat: responseFormat{Type: "json_object"},
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: string(userJSON)},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return core.Verdict{}, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := c.baseURL + "/chat/completions"
	if c.apiVersion != "" {
		endpoint += "?api-version=" + c.apiVersion
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return core.Verdict{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiVersion != "" {
		req.Header.Set("api-key", c.key)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	resp, err := c.client.Do(req) //nolint:gosec // G107: base URL is operator config; the API key travels in a header, not the URL
	if err != nil {
		return core.Verdict{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return core.Verdict{}, fmt.Errorf("chat completions status %d", resp.StatusCode)
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return core.Verdict{}, fmt.Errorf("decode response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return core.Verdict{}, fmt.Errorf("no choices in response")
	}

	// Record the real token counts and model on the active classify span. Only
	// metadata (never prompt/completion content) reaches the span; the telemetry
	// allowlist enforces this at export as defense in depth.
	c.recordGenAI(ctx, cr)

	var v core.Verdict
	content := cr.Choices[0].Message.Content
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return core.Verdict{}, fmt.Errorf("parse verdict json: %w", err)
	}
	return v, nil
}

// recordGenAI annotates the active span with OTel GenAI semantic-convention
// attributes: operation, system, request/response model, token usage, and the
// finish reason. No prompt or completion content is recorded. It also renames
// the span to the semconv form `chat {model}` so LLM-observability tooling
// recognizes the turn. When no span is active, SpanFromContext returns a no-op
// span and this is a cheap no-op.
func (c *OpenAICompat) recordGenAI(ctx context.Context, cr chatResponse) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetName("chat " + c.model)
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.system", "openai"),
		attribute.String("gen_ai.request.model", c.model),
		attribute.String("gen_ai.response.model", cr.Model),
		attribute.Int("gen_ai.usage.input_tokens", cr.Usage.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", cr.Usage.CompletionTokens),
	)
	if fr := cr.Choices[0].FinishReason; fr != "" {
		// semconv models this as an array (a response can have several choices).
		span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{fr}))
	}
}
