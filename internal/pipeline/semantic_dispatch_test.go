package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/cache"
	"github.com/eduardmaghakyan/qlite/internal/embedding"
	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/provider"
	"github.com/eduardmaghakyan/qlite/internal/qdrant"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

// mockUpstreamServer creates a mock OpenAI server for testing.
func mockUpstreamServer(resp *model.ChatResponse) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func mockEmbeddingServer(vector []float32, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": vector},
			},
		})
	}))
}

func mockQdrantServer(hit *model.ChatResponse, modelName string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test/points/search" {
			if hit != nil {
				payload, _ := json.Marshal(&qdrant.CachedPayload{Response: hit, Model: modelName})
				json.NewEncoder(w).Encode(map[string]any{
					"result": []map[string]any{
						{"id": "abc", "score": 0.99, "payload": json.RawMessage(payload)},
					},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
			}
			return
		}
		// Upsert
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
}

func newTestDispatch(upstreamURL string) *DispatchStage {
	p := provider.NewOpenAICompat("test", upstreamURL, "test-key", []string{"gpt-4o"})
	reg := provider.NewRegistry()
	reg.Register(p)
	reg.Freeze()
	return NewDispatchStage(reg, tokenizer.NewCounter())
}

func TestSemanticDispatch_SemanticHit(t *testing.T) {
	cachedResp := &model.ChatResponse{
		ID:    "semantic-cached",
		Model: "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Cached response"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}

	// Provider that's slow (semantic should win).
	providerResp := &model.ChatResponse{
		ID:    "provider-resp",
		Model: "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Provider response"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	slowProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(providerResp)
	}))
	defer slowProvider.Close()

	embServer := mockEmbeddingServer([]float32{0.1, 0.2, 0.3}, 0)
	defer embServer.Close()

	qdrantSrv := mockQdrantServer(cachedResp, "gpt-4o")
	defer qdrantSrv.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient(qdrantSrv.URL, "", "test")
	sc := cache.NewSemanticCache(embClient, qdrantClient, 0.95)

	dispatch := newTestDispatch(slowProvider.URL + "/v1")
	stage := NewSemanticDispatchStage(sc, dispatch, slog.Default())

	req := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:    "gpt-4o",
			Messages: []model.Message{{Role: "user", Content: "Hello"}},
		},
	}

	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ProviderName != "semantic_cache" {
		t.Errorf("expected provider semantic_cache, got %s", resp.ProviderName)
	}
	if resp.CacheStatus != "HIT" {
		t.Errorf("expected HIT, got %s", resp.CacheStatus)
	}
	if resp.Cost != 0 {
		t.Errorf("expected zero cost, got %f", resp.Cost)
	}
	if resp.ChatResponse.ID != "semantic-cached" {
		t.Errorf("expected semantic-cached, got %s", resp.ChatResponse.ID)
	}
}

func TestSemanticDispatch_SemanticMiss_DispatchWins(t *testing.T) {
	providerResp := &model.ChatResponse{
		ID:    "provider-resp",
		Model: "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Provider response"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	upstream := mockUpstreamServer(providerResp)
	defer upstream.Close()

	// Slow embedding = semantic will not win.
	embServer := mockEmbeddingServer([]float32{0.1, 0.2, 0.3}, 200*time.Millisecond)
	defer embServer.Close()

	qdrantSrv := mockQdrantServer(nil, "")
	defer qdrantSrv.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient(qdrantSrv.URL, "", "test")
	sc := cache.NewSemanticCache(embClient, qdrantClient, 0.95)

	dispatch := newTestDispatch(upstream.URL + "/v1")
	stage := NewSemanticDispatchStage(sc, dispatch, slog.Default())

	req := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:    "gpt-4o",
			Messages: []model.Message{{Role: "user", Content: "Hello"}},
		},
	}

	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ProviderName != "test" {
		t.Errorf("expected provider 'test', got %s", resp.ProviderName)
	}
	if resp.CacheStatus != "MISS" {
		t.Errorf("expected MISS, got %s", resp.CacheStatus)
	}
	if resp.ChatResponse.ID != "provider-resp" {
		t.Errorf("expected provider-resp, got %s", resp.ChatResponse.ID)
	}
}

func TestSemanticDispatch_SkipHighTemperature(t *testing.T) {
	providerResp := &model.ChatResponse{
		ID:    "provider-resp",
		Model: "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Response"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	upstream := mockUpstreamServer(providerResp)
	defer upstream.Close()

	// These should never be called.
	embServer := mockEmbeddingServer([]float32{0.1}, 0)
	defer embServer.Close()

	qdrantSrv := mockQdrantServer(nil, "")
	defer qdrantSrv.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient(qdrantSrv.URL, "", "test")
	sc := cache.NewSemanticCache(embClient, qdrantClient, 0.95)

	dispatch := newTestDispatch(upstream.URL + "/v1")
	stage := NewSemanticDispatchStage(sc, dispatch, slog.Default())

	temp := 0.7
	req := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:       "gpt-4o",
			Messages:    []model.Message{{Role: "user", Content: "Hello"}},
			Temperature: &temp,
		},
	}

	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should bypass semantic and go directly to dispatch.
	if resp.ProviderName != "test" {
		t.Errorf("expected direct dispatch, got %s", resp.ProviderName)
	}
}

func TestSemanticDispatch_StreamSemanticHit(t *testing.T) {
	cachedResp := &model.ChatResponse{
		ID:    "semantic-stream-cached",
		Model: "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Cached!"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}

	// Slow provider.
	slowProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer slowProvider.Close()

	embServer := mockEmbeddingServer([]float32{0.1, 0.2, 0.3}, 0)
	defer embServer.Close()

	qdrantSrv := mockQdrantServer(cachedResp, "gpt-4o")
	defer qdrantSrv.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient(qdrantSrv.URL, "", "test")
	sc := cache.NewSemanticCache(embClient, qdrantClient, 0.95)

	dispatch := newTestDispatch(slowProvider.URL + "/v1")
	stage := NewSemanticDispatchStage(sc, dispatch, slog.Default())

	sw := newTestSSEWriter()
	req := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:    "gpt-4o",
			Messages: []model.Message{{Role: "user", Content: "Hello"}},
			Stream:   true,
		},
	}

	resp, err := stage.ProcessStream(context.Background(), req, sw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ProviderName != "semantic_cache" {
		t.Errorf("expected semantic_cache, got %s", resp.ProviderName)
	}
	if !sw.done {
		t.Error("expected SSE Done() to be called")
	}
	if sw.headers["X-Cache"] != "HIT" {
		t.Errorf("expected X-Cache: HIT, got %s", sw.headers["X-Cache"])
	}
	if len(sw.events) == 0 {
		t.Error("expected SSE events to be written")
	}
}
