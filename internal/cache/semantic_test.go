package cache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eduardmaghakyan/qlite/internal/embedding"
	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/qdrant"
)

func TestSemanticCache_Lookup_Hit(t *testing.T) {
	cachedResp := &model.ChatResponse{
		ID:    "cached-id",
		Model: "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Hi!"}, FinishReason: "stop"},
		},
	}

	// Mock embedding server.
	embServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}},
			},
		})
	}))
	defer embServer.Close()

	// Mock qdrant server — returns a hit.
	payload, _ := json.Marshal(&qdrant.CachedPayload{Response: cachedResp, Model: "gpt-4o"})
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"result": []map[string]any{
				{"id": "abc", "score": 0.98, "payload": json.RawMessage(payload)},
			},
		})
	}))
	defer qdrantServer.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient(qdrantServer.URL, "", "test")
	sc := NewSemanticCache(embClient, qdrantClient, 0.95)

	req := &model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	resp, emb, err := sc.Lookup(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected cache hit")
	}
	if resp.ID != "cached-id" {
		t.Errorf("unexpected response ID: %s", resp.ID)
	}
	if emb == nil {
		t.Error("expected embedding to be returned")
	}
}

func TestSemanticCache_Lookup_Miss(t *testing.T) {
	// Mock embedding server.
	embServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}},
			},
		})
	}))
	defer embServer.Close()

	// Mock qdrant server — returns no results.
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
	}))
	defer qdrantServer.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient(qdrantServer.URL, "", "test")
	sc := NewSemanticCache(embClient, qdrantClient, 0.95)

	req := &model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	resp, emb, err := sc.Lookup(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Error("expected cache miss")
	}
	if emb == nil {
		t.Error("expected embedding even on miss")
	}
}

func TestSemanticCache_Lookup_EmbeddingError(t *testing.T) {
	// Mock embedding server — returns error.
	embServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer embServer.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient("http://unused:6333", "", "test")
	sc := NewSemanticCache(embClient, qdrantClient, 0.95)

	req := &model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	resp, emb, err := sc.Lookup(context.Background(), req)
	if err != nil {
		t.Fatalf("expected nil error for graceful fallthrough, got: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response on embedding error")
	}
	if emb != nil {
		t.Error("expected nil embedding on embedding error")
	}
}

func TestSemanticCache_Store(t *testing.T) {
	// Mock embedding server (not called since we pass emb).
	embServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("embedding should not be called when emb is provided")
	}))
	defer embServer.Close()

	upsertCalled := false
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upsertCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
	defer qdrantServer.Close()

	embClient := embedding.NewClient(embServer.URL, "key", "text-embedding-3-small")
	qdrantClient := qdrant.NewClient(qdrantServer.URL, "", "test")
	sc := NewSemanticCache(embClient, qdrantClient, 0.95)

	req := &model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}
	resp := &model.ChatResponse{ID: "resp-1"}

	err := sc.Store(context.Background(), req, resp, []float32{0.1, 0.2, 0.3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !upsertCalled {
		t.Error("expected qdrant upsert to be called")
	}
}
