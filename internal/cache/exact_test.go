package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

func ptrFloat(f float64) *float64 { return &f }

func makeReq(msg string, temp *float64, stream bool) *model.ChatRequest {
	return &model.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []model.Message{{Role: "user", Content: msg}},
		Temperature: temp,
		Stream:      stream,
	}
}

func makeResp(id string) *model.ChatResponse {
	return &model.ChatResponse{
		ID:     id,
		Object: "chat.completion",
		Model:  "gpt-4o",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "Hello!"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	c := New(time.Hour, 100)
	req := makeReq("hello", ptrFloat(0), false)
	resp := makeResp("test-1")

	c.Put(req, resp)

	entry, ok := c.Get(req)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if entry.Response.ID != "test-1" {
		t.Errorf("expected ID test-1, got %s", entry.Response.ID)
	}
}

func TestTTLExpiration(t *testing.T) {
	c := New(10*time.Millisecond, 100)
	req := makeReq("hello", ptrFloat(0), false)
	resp := makeResp("test-ttl")

	c.Put(req, resp)

	// Should hit immediately.
	if _, ok := c.Get(req); !ok {
		t.Fatal("expected cache hit before TTL")
	}

	time.Sleep(20 * time.Millisecond)

	// Should miss after TTL.
	if _, ok := c.Get(req); ok {
		t.Fatal("expected cache miss after TTL")
	}

	// Entry should be evicted.
	if c.Len() != 0 {
		t.Errorf("expected 0 entries after expiration, got %d", c.Len())
	}
}

func TestKeyExcludesStreamFlag(t *testing.T) {
	c := New(time.Hour, 100)
	req := makeReq("hello", ptrFloat(0), false)
	resp := makeResp("test-stream")

	c.Put(req, resp)

	// Same request but with stream=true should hit.
	streamReq := makeReq("hello", ptrFloat(0), true)
	entry, ok := c.Get(streamReq)
	if !ok {
		t.Fatal("expected cache hit for stream=true request")
	}
	if entry.Response.ID != "test-stream" {
		t.Errorf("expected ID test-stream, got %s", entry.Response.ID)
	}
}

func TestDifferentTemperaturesDifferentKeys(t *testing.T) {
	c := New(time.Hour, 100)
	req0 := makeReq("hello", ptrFloat(0), false)
	req1 := makeReq("hello", ptrFloat(0.7), false)

	c.Put(req0, makeResp("temp-0"))
	c.Put(req1, makeResp("temp-0.7"))

	entry0, ok := c.Get(req0)
	if !ok {
		t.Fatal("expected hit for temp=0")
	}
	if entry0.Response.ID != "temp-0" {
		t.Errorf("expected temp-0, got %s", entry0.Response.ID)
	}

	entry1, ok := c.Get(req1)
	if !ok {
		t.Fatal("expected hit for temp=0.7")
	}
	if entry1.Response.ID != "temp-0.7" {
		t.Errorf("expected temp-0.7, got %s", entry1.Response.ID)
	}
}

func TestMaxEntriesEviction(t *testing.T) {
	c := New(time.Hour, 3)

	for i := range 4 {
		req := makeReq("msg"+string(rune('a'+i)), ptrFloat(0), false)
		c.Put(req, makeResp("resp"))
	}

	if c.Len() != 3 {
		t.Errorf("expected 3 entries, got %d", c.Len())
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	c := New(time.Hour, 1000)
	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			req := makeReq("hello", ptrFloat(0), false)
			c.Put(req, makeResp("concurrent"))
		}(i)
		go func(i int) {
			defer wg.Done()
			req := makeReq("hello", ptrFloat(0), false)
			c.Get(req)
		}(i)
	}

	wg.Wait()
}

func TestCacheMiss(t *testing.T) {
	c := New(time.Hour, 100)
	req := makeReq("not-cached", ptrFloat(0), false)

	if _, ok := c.Get(req); ok {
		t.Fatal("expected cache miss for uncached request")
	}
}
