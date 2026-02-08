package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

func TestEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "text-embedding-3-small" {
			t.Errorf("unexpected model: %s", req.Model)
		}

		resp := embeddingResponse{
			Data: []struct {
				Embedding []float32 `json:"embedding"`
			}{
				{Embedding: []float32{0.1, 0.2, 0.3}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "text-embedding-3-small")
	emb, err := client.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(emb) != 3 {
		t.Fatalf("expected 3 dimensions, got %d", len(emb))
	}
	if emb[0] != 0.1 || emb[1] != 0.2 || emb[2] != 0.3 {
		t.Errorf("unexpected embedding values: %v", emb)
	}
}

func TestEmbed_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "text-embedding-3-small")
	_, err := client.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbed_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response — context should cancel before this returns.
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "text-embedding-3-small")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := client.Embed(ctx, "hello")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestTextFromMessages(t *testing.T) {
	messages := []model.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
	}
	text := TextFromMessages(messages)
	expected := "system: You are helpful.\nuser: Hello"
	if text != expected {
		t.Errorf("expected %q, got %q", expected, text)
	}
}

func TestTextFromMessages_Empty(t *testing.T) {
	text := TextFromMessages(nil)
	if text != "" {
		t.Errorf("expected empty string, got %q", text)
	}
}
