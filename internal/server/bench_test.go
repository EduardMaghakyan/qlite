package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/pipeline"
	"github.com/eduardmaghakyan/qlite/internal/provider"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

func TestProxyOverhead_P99(t *testing.T) {
	mockLatency := 5 * time.Millisecond

	expected := model.ChatResponse{
		ID:      "chatcmpl-bench",
		Object:  "chat.completion",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []model.Choice{
			{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: "Benchmark response"},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}
	respBytes, _ := json.Marshal(expected)

	// Mock upstream with fixed latency.
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(mockLatency)
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
	}))
	defer mockSrv.Close()

	counter := tokenizer.NewCounter()
	registry := provider.NewRegistry()
	p := provider.NewOpenAICompat("bench", mockSrv.URL, "bench-key", []string{"gpt-4o"})
	registry.Register(p)

	dispatch := pipeline.NewDispatchStage(registry, counter)
	pipe, _ := pipeline.New(dispatch)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHandler(pipe, counter, logger, nil)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	reqBody := model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	// Warm up tiktoken and HTTP connection.
	for range 5 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	// Measure direct latency.
	directLatencies := make([]time.Duration, 0, 100)
	client := &http.Client{}
	for range 100 {
		start := time.Now()
		resp, err := client.Post(mockSrv.URL+"/chat/completions", "application/json", bytes.NewReader(bodyBytes))
		if err != nil {
			t.Fatalf("direct request failed: %v", err)
		}
		resp.Body.Close()
		directLatencies = append(directLatencies, time.Since(start))
	}

	// Measure proxy latency.
	proxyLatencies := make([]time.Duration, 0, 100)
	for range 100 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(rec, req)
		proxyLatencies = append(proxyLatencies, time.Since(start))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	}

	sort.Slice(directLatencies, func(i, j int) bool { return directLatencies[i] < directLatencies[j] })
	sort.Slice(proxyLatencies, func(i, j int) bool { return proxyLatencies[i] < proxyLatencies[j] })

	directP99 := directLatencies[98]
	proxyP99 := proxyLatencies[98]
	overhead := proxyP99 - directP99

	t.Logf("Direct P99: %v", directP99)
	t.Logf("Proxy P99:  %v", proxyP99)
	t.Logf("Overhead:   %v", overhead)

	maxOverhead := 10 * time.Millisecond
	if overhead > maxOverhead {
		t.Errorf("P99 overhead %v exceeds %v", overhead, maxOverhead)
	}

	// Also report P50.
	directP50 := directLatencies[49]
	proxyP50 := proxyLatencies[49]
	t.Logf("Direct P50: %v", directP50)
	t.Logf("Proxy P50:  %v", proxyP50)
	t.Logf("P50 Overhead: %v", proxyP50-directP50)

	fmt.Printf("Performance: P99 overhead = %v (limit: %v)\n", overhead, maxOverhead)
}

func TestProxyOverhead_P99_Concurrent(t *testing.T) {
	mockLatency := 5 * time.Millisecond

	expected := model.ChatResponse{
		ID:      "chatcmpl-bench",
		Object:  "chat.completion",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []model.Choice{
			{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: "Benchmark response"},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}
	respBytes, _ := json.Marshal(expected)

	// Mock upstream with fixed latency.
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(mockLatency)
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
	}))
	defer mockSrv.Close()

	counter := tokenizer.NewCounter()
	registry := provider.NewRegistry()
	p := provider.NewOpenAICompat("bench", mockSrv.URL, "bench-key", []string{"gpt-4o"})
	registry.Register(p)

	dispatch := pipeline.NewDispatchStage(registry, counter)
	pipe, _ := pipeline.New(dispatch)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHandler(pipe, counter, logger, nil)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	reqBody := model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	// Warm up.
	for range 10 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	// Measure direct latency concurrently.
	const totalRequests = 100
	const concurrency = 20

	directLatencies := make([]time.Duration, totalRequests)
	client := &http.Client{}
	var wgDirect sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i := range totalRequests {
		wgDirect.Add(1)
		go func(idx int) {
			defer wgDirect.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			resp, err := client.Post(mockSrv.URL+"/chat/completions", "application/json", bytes.NewReader(bodyBytes))
			if err != nil {
				t.Errorf("direct request failed: %v", err)
				return
			}
			resp.Body.Close()
			directLatencies[idx] = time.Since(start)
		}(i)
	}
	wgDirect.Wait()

	// Measure proxy latency concurrently.
	proxyLatencies := make([]time.Duration, totalRequests)
	var wgProxy sync.WaitGroup
	sem2 := make(chan struct{}, concurrency)
	for i := range totalRequests {
		wgProxy.Add(1)
		go func(idx int) {
			defer wgProxy.Done()
			sem2 <- struct{}{}
			defer func() { <-sem2 }()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			start := time.Now()
			mux.ServeHTTP(rec, req)
			proxyLatencies[idx] = time.Since(start)
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rec.Code)
			}
		}(i)
	}
	wgProxy.Wait()

	sort.Slice(directLatencies, func(i, j int) bool { return directLatencies[i] < directLatencies[j] })
	sort.Slice(proxyLatencies, func(i, j int) bool { return proxyLatencies[i] < proxyLatencies[j] })

	directP99 := directLatencies[98]
	proxyP99 := proxyLatencies[98]
	overhead := proxyP99 - directP99

	t.Logf("Direct P99: %v", directP99)
	t.Logf("Proxy P99:  %v", proxyP99)
	t.Logf("Overhead:   %v", overhead)

	directP50 := directLatencies[49]
	proxyP50 := proxyLatencies[49]
	t.Logf("Direct P50: %v", directP50)
	t.Logf("Proxy P50:  %v", proxyP50)
	t.Logf("P50 Overhead: %v", proxyP50-directP50)

	maxOverhead := 10 * time.Millisecond
	if overhead > maxOverhead {
		t.Errorf("P99 concurrent overhead %v exceeds %v", overhead, maxOverhead)
	}

	fmt.Printf("Performance (concurrent): P99 overhead = %v (limit: %v)\n", overhead, maxOverhead)
}
