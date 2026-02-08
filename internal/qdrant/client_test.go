package qdrant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

func TestEnsureCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/collections/test_collection" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test_collection")
	err := client.EnsureCollection(context.Background(), 1536)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureCollection_AlreadyExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"status":{"error":"already exists"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test_collection")
	err := client.EnsureCollection(context.Background(), 1536)
	if err != nil {
		t.Fatalf("409 should not error: %v", err)
	}
}

func TestSearch(t *testing.T) {
	resp := &model.ChatResponse{
		ID:    "test-id",
		Model: "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Hello!"}, FinishReason: "stop"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/collections/test_collection/points/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req searchRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Filter == nil || len(req.Filter.Must) == 0 {
			t.Error("expected model filter")
		}
		if req.Filter.Must[0].Match.Value != "gpt-4o" {
			t.Errorf("unexpected model filter: %s", req.Filter.Must[0].Match.Value)
		}

		payload, _ := json.Marshal(&CachedPayload{Response: resp, Model: "gpt-4o", CreatedAt: 1000})

		result := searchResponse{
			Result: []searchResultRaw{
				{ID: "abc", Score: 0.98, Payload: payload},
			},
		}
		json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test_collection")
	results, err := client.Search(context.Background(), []float32{0.1, 0.2}, 1, 0.95, "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Score != 0.98 {
		t.Errorf("unexpected score: %f", results[0].Score)
	}
	if results[0].Payload.Response.ID != "test-id" {
		t.Errorf("unexpected response ID: %s", results[0].Payload.Response.ID)
	}
}

func TestSearch_NoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(searchResponse{Result: nil})
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test_collection")
	results, err := client.Search(context.Background(), []float32{0.1}, 1, 0.95, "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestUpsert(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/collections/test_collection/points" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req upsertRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Points) != 1 {
			t.Errorf("expected 1 point, got %d", len(req.Points))
		}
		if req.Points[0].ID != "test-point" {
			t.Errorf("unexpected point ID: %s", req.Points[0].ID)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "test_collection")
	err := client.Upsert(context.Background(), "test-point", []float32{0.1, 0.2}, &CachedPayload{
		Response: &model.ChatResponse{ID: "resp-1"},
		Model:    "gpt-4o",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIKeyHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "my-qdrant-key" {
			t.Errorf("expected api-key header, got %q", r.Header.Get("api-key"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "my-qdrant-key", "test_collection")
	err := client.EnsureCollection(context.Background(), 1536)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
