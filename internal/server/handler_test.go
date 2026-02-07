package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/pipeline"
	"github.com/eduardmaghakyan/qlite/internal/provider"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

func setupTestHandler(t *testing.T, mockSrv *httptest.Server) *Handler {
	t.Helper()

	counter := tokenizer.NewCounter()
	registry := provider.NewRegistry()
	p := provider.NewOpenAICompat("test", mockSrv.URL, "test-key", []string{"gpt-4o", "gpt-4o-mini"})
	registry.Register(p)

	dispatch := pipeline.NewDispatchStage(registry, counter)
	pipe, err := pipeline.New(dispatch)
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewHandler(pipe, counter, logger)
}

func TestHandler_NonStreaming(t *testing.T) {
	expected := model.ChatResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []model.Choice{
			{
				Index:        0,
				Message:      model.Message{Role: "assistant", Content: "Hi there!"},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     12,
			CompletionTokens: 8,
			TotalTokens:      20,
		},
	}

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer mockSrv.Close()

	handler := setupTestHandler(t, mockSrv)

	reqBody := model.ChatRequest{
		Model: "gpt-4o",
		Messages: []model.Message{
			{Role: "user", Content: "Hello!"},
		},
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if rec.Header().Get("X-Cache") != "MISS" {
		t.Errorf("expected X-Cache MISS, got %q", rec.Header().Get("X-Cache"))
	}
	if rec.Header().Get("X-Provider") != "test" {
		t.Errorf("expected X-Provider test, got %q", rec.Header().Get("X-Provider"))
	}
	if rec.Header().Get("X-Tokens-Input") == "" {
		t.Error("expected X-Tokens-Input header")
	}
	if rec.Header().Get("X-Tokens-Output") == "" {
		t.Error("expected X-Tokens-Output header")
	}
	if rec.Header().Get("X-Request-Cost") == "" {
		t.Error("expected X-Request-Cost header")
	}

	var resp model.ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ID != expected.ID {
		t.Errorf("expected ID %s, got %s", expected.ID, resp.ID)
	}
}

func TestHandler_Streaming(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":""}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":""}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
	}

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		for _, chunk := range chunks {
			w.Write([]byte("data: " + chunk + "\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer mockSrv.Close()

	handler := setupTestHandler(t, mockSrv)

	reqBody := model.ChatRequest{
		Model:  "gpt-4o",
		Stream: true,
		Messages: []model.Message{
			{Role: "user", Content: "Hello!"},
		},
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %q", rec.Header().Get("Content-Type"))
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "data: ") {
		t.Error("expected SSE data events in response")
	}
	if !strings.Contains(respBody, "[DONE]") {
		t.Error("expected [DONE] event in response")
	}
}

func TestHandler_InvalidRequest(t *testing.T) {
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called")
	}))
	defer mockSrv.Close()

	handler := setupTestHandler(t, mockSrv)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Missing model.
	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	var errResp model.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("expected error type invalid_request_error, got %q", errResp.Error.Type)
	}
}

func TestHandler_InvalidJSON(t *testing.T) {
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called")
	}))
	defer mockSrv.Close()

	handler := setupTestHandler(t, mockSrv)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_Health(t *testing.T) {
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockSrv.Close()

	handler := setupTestHandler(t, mockSrv)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Error("expected health response with ok status")
	}
}

func TestHandler_UnknownModel(t *testing.T) {
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called for unknown model")
	}))
	defer mockSrv.Close()

	handler := setupTestHandler(t, mockSrv)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	reqBody := `{"model":"unknown-model","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}
}

func TestMiddleware_RequestID(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := GetRequestID(r.Context())
		if id == "" {
			t.Error("expected request ID in context")
		}
		w.WriteHeader(http.StatusOK)
	})

	wrapped := RequestID(handler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
}

func TestMiddleware_CORS(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := CORS(handler)

	// Test preflight.
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS allow-origin header")
	}
}

func TestMiddleware_Recovery(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	wrapped := Recovery(logger)(handler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}
