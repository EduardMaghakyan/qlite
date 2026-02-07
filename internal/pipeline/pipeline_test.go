package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/provider"
	"github.com/eduardmaghakyan/qlite/internal/sse"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

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

func (w *testSSEWriter) SetHeader(key, value string) { w.headers[key] = value }
func (w *testSSEWriter) WriteEvent(data []byte) error {
	w.events = append(w.events, string(data))
	return nil
}
func (w *testSSEWriter) Done() error {
	w.done = true
	return nil
}

var _ sse.Writer = (*testSSEWriter)(nil)

func TestPipeline_Execute(t *testing.T) {
	expected := model.ChatResponse{
		ID:      "chatcmpl-pipe",
		Object:  "chat.completion",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []model.Choice{
			{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: "Pipeline works!"},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer mockSrv.Close()

	registry := provider.NewRegistry()
	p := provider.NewOpenAICompat("test", mockSrv.URL, "test-key", []string{"gpt-4o"})
	registry.Register(p)

	counter := tokenizer.NewCounter()
	dispatch := NewDispatchStage(registry, counter)
	pipe, err := New(dispatch)
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	proxyReq := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:    "gpt-4o",
			Messages: []model.Message{{Role: "user", Content: "Hello"}},
		},
		RequestID:   "test-123",
		ReceivedAt:  time.Now(),
		InputTokens: 5,
	}

	resp, err := pipe.Execute(context.Background(), proxyReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ChatResponse.ID != expected.ID {
		t.Errorf("expected ID %s, got %s", expected.ID, resp.ChatResponse.ID)
	}
	if resp.CacheStatus != "MISS" {
		t.Errorf("expected cache MISS, got %s", resp.CacheStatus)
	}
	if resp.ProviderName != "test" {
		t.Errorf("expected provider 'test', got %q", resp.ProviderName)
	}
	if resp.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", resp.OutputTokens)
	}
	if resp.Cost <= 0 {
		t.Error("expected positive cost")
	}
}

func TestPipeline_ExecuteStream(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
	}

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockSrv.Close()

	registry := provider.NewRegistry()
	p := provider.NewOpenAICompat("test", mockSrv.URL, "test-key", []string{"gpt-4o"})
	registry.Register(p)

	counter := tokenizer.NewCounter()
	dispatch := NewDispatchStage(registry, counter)
	pipe, err := New(dispatch)
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	proxyReq := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:    "gpt-4o",
			Messages: []model.Message{{Role: "user", Content: "Hello"}},
		},
		RequestID:   "test-stream-123",
		ReceivedAt:  time.Now(),
		InputTokens: 5,
	}

	sw := newTestSSEWriter()
	resp, err := pipe.ExecuteStream(context.Background(), proxyReq, sw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sw.done {
		t.Error("expected Done to be called")
	}
	if len(sw.events) != 3 {
		t.Errorf("expected 3 events, got %d", len(sw.events))
	}
	if resp.OutputTokens != 2 {
		t.Errorf("expected 2 output tokens, got %d", resp.OutputTokens)
	}
	if resp.CacheStatus != "MISS" {
		t.Errorf("expected cache MISS, got %s", resp.CacheStatus)
	}
}

func TestPipeline_InvalidStage(t *testing.T) {
	_, err := New("not a stage")
	if err == nil {
		t.Error("expected error for invalid stage")
	}
}

func TestPipeline_UnknownModel(t *testing.T) {
	registry := provider.NewRegistry()
	counter := tokenizer.NewCounter()
	dispatch := NewDispatchStage(registry, counter)
	pipe, _ := New(dispatch)

	proxyReq := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:    "unknown-model",
			Messages: []model.Message{{Role: "user", Content: "Hello"}},
		},
		RequestID:  "test-404",
		ReceivedAt: time.Now(),
	}

	_, err := pipe.Execute(context.Background(), proxyReq)
	if err == nil {
		t.Error("expected error for unknown model")
	}
}
