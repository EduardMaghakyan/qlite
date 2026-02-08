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
)

func TestGoogle_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/models/gemini-2.5-flash:generateContent") {
			t.Errorf("expected path to contain model name, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("expected key=test-key, got %s", r.URL.Query().Get("key"))
		}
		// Should NOT have Authorization header.
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header, got %s", r.Header.Get("Authorization"))
		}

		var gr geminiRequest
		if err := json.NewDecoder(r.Body).Decode(&gr); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(geminiResponse{
			Candidates: []geminiCandidate{
				{
					Content:      geminiContent{Role: "model", Parts: []geminiPart{{Text: "Hello!"}}},
					FinishReason: "STOP",
				},
			},
			UsageMetadata: &geminiUsage{
				PromptTokenCount:     10,
				CandidatesTokenCount: 5,
				TotalTokenCount:      15,
			},
		})
	}))
	defer srv.Close()

	p := NewGoogle("google", srv.URL, "test-key", []string{"gemini-2.5-flash"})

	req := &model.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []model.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	resp, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Object != "chat.completion" {
		t.Errorf("expected object chat.completion, got %s", resp.Object)
	}
	if !strings.HasPrefix(resp.ID, "gen-") {
		t.Errorf("expected ID to start with gen-, got %s", resp.ID)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("expected 10 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("expected 5 completion tokens, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("expected 15 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestGoogle_Chat_SystemMessage(t *testing.T) {
	var capturedRequest geminiRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedRequest)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(geminiResponse{
			Candidates: []geminiCandidate{
				{
					Content:      geminiContent{Role: "model", Parts: []geminiPart{{Text: "OK"}}},
					FinishReason: "STOP",
				},
			},
			UsageMetadata: &geminiUsage{PromptTokenCount: 5, CandidatesTokenCount: 2, TotalTokenCount: 7},
		})
	}))
	defer srv.Close()

	p := NewGoogle("google", srv.URL, "test-key", []string{"gemini-2.5-flash"})

	req := &model.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []model.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi"},
		},
	}

	_, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedRequest.SystemInstruction == nil {
		t.Fatal("expected systemInstruction to be set")
	}
	if len(capturedRequest.SystemInstruction.Parts) != 1 {
		t.Fatalf("expected 1 part in systemInstruction, got %d", len(capturedRequest.SystemInstruction.Parts))
	}
	if capturedRequest.SystemInstruction.Parts[0].Text != "You are helpful." {
		t.Errorf("expected system text 'You are helpful.', got %q", capturedRequest.SystemInstruction.Parts[0].Text)
	}
	// System message should not appear in contents.
	if len(capturedRequest.Contents) != 1 {
		t.Fatalf("expected 1 content (system extracted), got %d", len(capturedRequest.Contents))
	}
	if capturedRequest.Contents[0].Role != "user" {
		t.Errorf("expected user role, got %s", capturedRequest.Contents[0].Role)
	}
}

func TestGoogle_Chat_RoleMapping(t *testing.T) {
	var capturedRequest geminiRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedRequest)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(geminiResponse{
			Candidates: []geminiCandidate{
				{
					Content:      geminiContent{Role: "model", Parts: []geminiPart{{Text: "OK"}}},
					FinishReason: "STOP",
				},
			},
			UsageMetadata: &geminiUsage{PromptTokenCount: 10, CandidatesTokenCount: 2, TotalTokenCount: 12},
		})
	}))
	defer srv.Close()

	p := NewGoogle("google", srv.URL, "test-key", []string{"gemini-2.5-flash"})

	req := &model.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []model.Message{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
			{Role: "user", Content: "How are you?"},
		},
	}

	_, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedRequest.Contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(capturedRequest.Contents))
	}
	// "assistant" should be mapped to "model".
	if capturedRequest.Contents[1].Role != "model" {
		t.Errorf("expected role 'model' for assistant message, got %q", capturedRequest.Contents[1].Role)
	}
	if capturedRequest.Contents[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", capturedRequest.Contents[0].Role)
	}
}

func TestGoogle_Chat_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"API key invalid","status":"PERMISSION_DENIED"}}`)
	}))
	defer srv.Close()

	p := NewGoogle("google", srv.URL, "test-key", []string{"gemini-2.5-flash"})
	req := &model.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := p.Chat(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for upstream error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to contain status code 403, got: %v", err)
	}
}

func TestGoogle_ChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("expected streaming URL path, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("expected alt=sse, got %s", r.URL.Query().Get("alt"))
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		// Chunk 1: partial content.
		fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`+"\n\n")
		flusher.Flush()

		// Chunk 2: more content with finish reason.
		fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`+"\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewGoogle("google", srv.URL, "test-key", []string{"gemini-2.5-flash"})
	req := &model.ChatRequest{
		Model:    "gemini-2.5-flash",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	sw := newTestSSEWriter()
	usage, err := p.ChatStream(context.Background(), req, sw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sw.done {
		t.Error("expected Done to be called")
	}

	// Expect 3 events: role chunk + 2 content chunks.
	if len(sw.events) != 3 {
		t.Errorf("expected 3 events, got %d", len(sw.events))
		for i, e := range sw.events {
			t.Logf("event[%d]: %s", i, e)
		}
	}

	// Verify first chunk has role.
	var firstChunk model.ChatStreamChunk
	if err := json.Unmarshal([]byte(sw.events[0]), &firstChunk); err != nil {
		t.Fatalf("failed to unmarshal first chunk: %v", err)
	}
	if firstChunk.Choices[0].Delta.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", firstChunk.Choices[0].Delta.Role)
	}

	// Verify content chunk.
	var contentChunk model.ChatStreamChunk
	if err := json.Unmarshal([]byte(sw.events[1]), &contentChunk); err != nil {
		t.Fatalf("failed to unmarshal content chunk: %v", err)
	}
	if contentChunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", contentChunk.Choices[0].Delta.Content)
	}

	// Verify last chunk has finish_reason.
	var lastChunk model.ChatStreamChunk
	if err := json.Unmarshal([]byte(sw.events[2]), &lastChunk); err != nil {
		t.Fatalf("failed to unmarshal last chunk: %v", err)
	}
	if lastChunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", lastChunk.Choices[0].FinishReason)
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
	if usage.TotalTokens != 15 {
		t.Errorf("expected 15 total tokens, got %d", usage.TotalTokens)
	}
}

func TestGoogle_FinishReasonMapping(t *testing.T) {
	tests := []struct {
		geminiReason string
		openaiReason string
	}{
		{"STOP", "stop"},
		{"MAX_TOKENS", "length"},
		{"SAFETY", "content_filter"},
		{"OTHER", "OTHER"},
	}

	for _, tt := range tests {
		t.Run(tt.geminiReason, func(t *testing.T) {
			got := geminiFinishReason(tt.geminiReason)
			if got != tt.openaiReason {
				t.Errorf("geminiFinishReason(%q) = %q, want %q", tt.geminiReason, got, tt.openaiReason)
			}
		})
	}
}
