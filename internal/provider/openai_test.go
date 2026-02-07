package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

func TestOpenAICompat_Chat(t *testing.T) {
	expected := model.ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []model.Choice{
			{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: "Hello!"},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}

		var req model.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Stream {
			t.Error("expected stream to be false")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer srv.Close()

	provider := NewOpenAICompat("test", srv.URL, "test-key", []string{"gpt-4o"})

	req := &model.ChatRequest{
		Model: "gpt-4o",
		Messages: []model.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	resp, err := provider.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != expected.ID {
		t.Errorf("expected ID %s, got %s", expected.ID, resp.ID)
	}
	if resp.Usage.PromptTokens != expected.Usage.PromptTokens {
		t.Errorf("expected prompt tokens %d, got %d", expected.Usage.PromptTokens, resp.Usage.PromptTokens)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %q", resp.Choices[0].Message.Content)
	}
}

func TestOpenAICompat_Chat_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer srv.Close()

	provider := NewOpenAICompat("test", srv.URL, "test-key", []string{"gpt-4o"})
	req := &model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := provider.Chat(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for upstream error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected error to contain status code 429, got: %v", err)
	}
}

// testSSEWriter captures SSE events for testing.
type testSSEWriter struct {
	events  []string
	headers map[string]string
	done    bool
}

func newTestSSEWriter() *testSSEWriter {
	return &testSSEWriter{
		headers: make(map[string]string),
	}
}

func (w *testSSEWriter) SetHeader(key, value string) {
	w.headers[key] = value
}

func (w *testSSEWriter) WriteEvent(data []byte) error {
	w.events = append(w.events, string(data))
	return nil
}

func (w *testSSEWriter) Done() error {
	w.done = true
	return nil
}

func TestOpenAICompat_ChatStream(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req model.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			t.Error("expected stream to be true")
		}
		if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Error("expected stream_options.include_usage to be true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	provider := NewOpenAICompat("test", srv.URL, "test-key", []string{"gpt-4o"})
	req := &model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	sw := newTestSSEWriter()
	usage, err := provider.ChatStream(context.Background(), req, sw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sw.done {
		t.Error("expected Done to be called")
	}
	if len(sw.events) != 4 {
		t.Errorf("expected 4 events, got %d", len(sw.events))
	}
	if usage == nil {
		t.Fatal("expected usage to be non-nil")
	}
	if usage.PromptTokens != 10 {
		t.Errorf("expected 10 prompt tokens, got %d", usage.PromptTokens)
	}
	if usage.CompletionTokens != 5 {
		t.Errorf("expected 5 completion tokens, got %d", usage.CompletionTokens)
	}
}

func TestRegistry_LookupAndRegister(t *testing.T) {
	registry := NewRegistry()

	p := NewOpenAICompat("openai", "https://api.openai.com/v1", "key", []string{"gpt-4o", "gpt-4o-mini"})
	registry.Register(p)

	found, err := registry.Lookup("gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Name() != "openai" {
		t.Errorf("expected provider name 'openai', got %q", found.Name())
	}

	found, err = registry.Lookup("gpt-4o-mini")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Name() != "openai" {
		t.Errorf("expected provider name 'openai', got %q", found.Name())
	}

	_, err = registry.Lookup("unknown-model")
	if err == nil {
		t.Error("expected error for unknown model")
	}
}

// Ensure testSSEWriter implements sse.Writer.
var _ sse.Writer = (*testSSEWriter)(nil)
