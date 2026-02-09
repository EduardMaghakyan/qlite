package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/cache"
	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/provider"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

func ptrFloat(f float64) *float64 { return &f }

func cachedResponse() *model.ChatResponse {
	return &model.ChatResponse{
		ID:     "chatcmpl-cached",
		Object: "chat.completion",
		Model:  "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Cached!"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

func TestCacheStage_Miss(t *testing.T) {
	c := cache.New(time.Hour, 100)
	stage := NewCacheStage(c, true)

	req := &model.ProxyRequest{
		ChatRequest: model.ChatRequest{
			Model:       "gpt-4o",
			Messages:    []model.Message{{Role: "user", Content: "hello"}},
			Temperature: ptrFloat(0),
		},
	}

	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response on cache miss")
	}
}

func TestCacheStage_HitNonStreaming(t *testing.T) {
	c := cache.New(time.Hour, 100)
	stage := NewCacheStage(c, true)

	chatReq := model.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []model.Message{{Role: "user", Content: "hello"}},
		Temperature: ptrFloat(0),
	}

	// Pre-populate cache.
	c.Put(&chatReq, cachedResponse())

	req := &model.ProxyRequest{ChatRequest: chatReq}
	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response on cache hit")
	}
	if resp.CacheStatus != "HIT" {
		t.Errorf("expected CacheStatus HIT, got %s", resp.CacheStatus)
	}
	if resp.Cost != 0 {
		t.Errorf("expected cost 0, got %f", resp.Cost)
	}
	if resp.ProviderName != "cache" {
		t.Errorf("expected provider cache, got %s", resp.ProviderName)
	}
	if resp.ChatResponse.ID != "chatcmpl-cached" {
		t.Errorf("expected cached response ID, got %s", resp.ChatResponse.ID)
	}
}

func TestCacheStage_HitStreaming(t *testing.T) {
	c := cache.New(time.Hour, 100)
	stage := NewCacheStage(c, true)

	chatReq := model.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []model.Message{{Role: "user", Content: "hello"}},
		Temperature: ptrFloat(0),
		Stream:      true,
	}

	// Pre-populate cache (stream flag excluded from key).
	nonStreamReq := chatReq
	nonStreamReq.Stream = false
	c.Put(&nonStreamReq, cachedResponse())

	req := &model.ProxyRequest{ChatRequest: chatReq}
	sw := newTestSSEWriter()

	resp, err := stage.ProcessStream(context.Background(), req, sw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response on streaming cache hit")
	}
	if resp.CacheStatus != "HIT" {
		t.Errorf("expected CacheStatus HIT, got %s", resp.CacheStatus)
	}
	if !sw.done {
		t.Error("expected Done to be called")
	}
	// Role chunk + content chunk + finish chunk = 3 events.
	if len(sw.events) != 3 {
		t.Errorf("expected 3 SSE events, got %d", len(sw.events))
	}
	if sw.headers["X-Cache"] != "HIT" {
		t.Errorf("expected X-Cache HIT header, got %q", sw.headers["X-Cache"])
	}
}

func TestCacheStage_TemperatureAboveZeroSkips(t *testing.T) {
	c := cache.New(time.Hour, 100)
	stage := NewCacheStage(c, true)

	chatReq := model.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []model.Message{{Role: "user", Content: "hello"}},
		Temperature: ptrFloat(0.7),
	}

	// Even if cached, high temperature should skip.
	c.Put(&chatReq, cachedResponse())

	req := &model.ProxyRequest{ChatRequest: chatReq}
	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response when temperature > 0")
	}
}

func TestCacheStage_NilTemperatureUsesCache(t *testing.T) {
	c := cache.New(time.Hour, 100)
	stage := NewCacheStage(c, true)

	chatReq := model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "hello"}},
		// Temperature is nil (default / omitted by client).
	}

	c.Put(&chatReq, cachedResponse())

	req := &model.ProxyRequest{ChatRequest: chatReq}
	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected cache hit when temperature is nil")
	}
	if resp.CacheStatus != "HIT" {
		t.Errorf("expected CacheStatus HIT, got %s", resp.CacheStatus)
	}
}

func TestCacheStage_TemperatureZeroUsesCache(t *testing.T) {
	c := cache.New(time.Hour, 100)
	stage := NewCacheStage(c, true)

	chatReq := model.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []model.Message{{Role: "user", Content: "hello"}},
		Temperature: ptrFloat(0),
	}

	c.Put(&chatReq, cachedResponse())

	req := &model.ProxyRequest{ChatRequest: chatReq}
	resp, err := stage.Process(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected cache hit for temperature=0")
	}
}

func TestCacheStage_FullPipelineIntegration(t *testing.T) {
	expected := model.ChatResponse{
		ID:      "chatcmpl-integration",
		Object:  "chat.completion",
		Model:   "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Hello!"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}

	callCount := 0
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer mockSrv.Close()

	c := cache.New(time.Hour, 100)

	registry := provider.NewRegistry()
	p := provider.NewOpenAICompat("test", mockSrv.URL, "test-key", []string{"gpt-4o"})
	registry.Register(p)

	counter := tokenizer.NewCounter()
	dispatch := NewDispatchStage(registry, counter)
	cacheStage := NewCacheStage(c, true)

	pipe, err := New(cacheStage, dispatch)
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	chatReq := model.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []model.Message{{Role: "user", Content: "hello"}},
		Temperature: ptrFloat(0),
	}

	// First request — cache miss, dispatches to upstream.
	req1 := &model.ProxyRequest{
		ChatRequest: chatReq,
		RequestID:   "req-1",
	}
	resp1, err := pipe.Execute(context.Background(), req1)
	if err != nil {
		t.Fatalf("first request error: %v", err)
	}
	if resp1.CacheStatus != "MISS" {
		t.Errorf("expected MISS on first request, got %s", resp1.CacheStatus)
	}
	if callCount != 1 {
		t.Errorf("expected 1 upstream call, got %d", callCount)
	}

	// Simulate handler storing response in cache on miss.
	c.Put(&chatReq, resp1.ChatResponse)

	// Second identical request — should hit cache.
	req2 := &model.ProxyRequest{
		ChatRequest: chatReq,
		RequestID:   "req-2",
	}
	resp2, err := pipe.Execute(context.Background(), req2)
	if err != nil {
		t.Fatalf("second request error: %v", err)
	}
	if resp2.CacheStatus != "HIT" {
		t.Errorf("expected HIT on second request, got %s", resp2.CacheStatus)
	}
	if resp2.Cost != 0 {
		t.Errorf("expected cost 0 on cache hit, got %f", resp2.Cost)
	}
	// Upstream should NOT have been called again.
	if callCount != 1 {
		t.Errorf("expected still 1 upstream call, got %d", callCount)
	}
}
