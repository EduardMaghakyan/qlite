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

func TestAnthropic_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/messages" {
			t.Errorf("expected /messages, got %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key test-key, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version 2023-06-01, got %s", r.Header.Get("anthropic-version"))
		}

		var ar anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&ar); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if ar.Stream {
			t.Error("expected stream to be false")
		}
		if ar.MaxTokens != 100 {
			t.Errorf("expected max_tokens 100, got %d", ar.MaxTokens)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:         "msg_123",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-sonnet-4-5",
			StopReason: "end_turn",
			Content: []anthropicContent{
				{Type: "text", Text: "Hello!"},
			},
			Usage: anthropicUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
		})
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic", srv.URL, "test-key", []string{"claude-sonnet-4-5"})

	maxTokens := 100
	req := &model.ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []model.Message{
			{Role: "user", Content: "Hello"},
		},
		MaxTokens: &maxTokens,
	}

	resp, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "msg_123" {
		t.Errorf("expected ID msg_123, got %s", resp.ID)
	}
	if resp.Object != "chat.completion" {
		t.Errorf("expected object chat.completion, got %s", resp.Object)
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

func TestAnthropic_Chat_SystemMessage(t *testing.T) {
	var capturedRequest anthropicRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedRequest)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:         "msg_456",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-sonnet-4-5",
			StopReason: "end_turn",
			Content:    []anthropicContent{{Type: "text", Text: "OK"}},
			Usage:      anthropicUsage{InputTokens: 5, OutputTokens: 2},
		})
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic", srv.URL, "test-key", []string{"claude-sonnet-4-5"})

	req := &model.ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []model.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi"},
		},
	}

	_, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedRequest.System != "You are helpful." {
		t.Errorf("expected system 'You are helpful.', got %q", capturedRequest.System)
	}
	if len(capturedRequest.Messages) != 1 {
		t.Fatalf("expected 1 message (system extracted), got %d", len(capturedRequest.Messages))
	}
	if capturedRequest.Messages[0].Role != "user" {
		t.Errorf("expected user message, got %s", capturedRequest.Messages[0].Role)
	}
}

func TestAnthropic_Chat_DefaultMaxTokens(t *testing.T) {
	var capturedRequest anthropicRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedRequest)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(anthropicResponse{
			ID:         "msg_789",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-sonnet-4-5",
			StopReason: "end_turn",
			Content:    []anthropicContent{{Type: "text", Text: "OK"}},
			Usage:      anthropicUsage{InputTokens: 5, OutputTokens: 2},
		})
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic", srv.URL, "test-key", []string{"claude-sonnet-4-5"})

	req := &model.ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []model.Message{
			{Role: "user", Content: "Hi"},
		},
		// MaxTokens is nil — should default to 4096.
	}

	_, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedRequest.MaxTokens != 4096 {
		t.Errorf("expected default max_tokens 4096, got %d", capturedRequest.MaxTokens)
	}
}

func TestAnthropic_Chat_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic", srv.URL, "test-key", []string{"claude-sonnet-4-5"})
	req := &model.ChatRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []model.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := p.Chat(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for upstream error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected error to contain status code 429, got: %v", err)
	}
}

func TestAnthropic_ChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ar anthropicRequest
		json.NewDecoder(r.Body).Decode(&ar)
		if !ar.Stream {
			t.Error("expected stream to be true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		// message_start
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n")
		flusher.Flush()

		// content_block_start (should be skipped)
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		flusher.Flush()

		// ping (should be skipped)
		fmt.Fprint(w, "event: ping\n")
		fmt.Fprint(w, `data: {"type":"ping"}`+"\n\n")
		flusher.Flush()

		// content_block_delta
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`+"\n\n")
		flusher.Flush()

		// content_block_delta
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}`+"\n\n")
		flusher.Flush()

		// content_block_stop (should be skipped)
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		flusher.Flush()

		// message_delta
		fmt.Fprint(w, "event: message_delta\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`+"\n\n")
		flusher.Flush()

		// message_stop
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic", srv.URL, "test-key", []string{"claude-sonnet-4-5"})
	req := &model.ChatRequest{
		Model:    "claude-sonnet-4-5",
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

	// Expect 4 events: message_start role chunk, 2 content deltas, message_delta finish
	if len(sw.events) != 4 {
		t.Errorf("expected 4 events, got %d", len(sw.events))
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
	if firstChunk.ID != "msg_stream" {
		t.Errorf("expected ID 'msg_stream', got %q", firstChunk.ID)
	}

	// Verify content delta.
	var contentChunk model.ChatStreamChunk
	if err := json.Unmarshal([]byte(sw.events[1]), &contentChunk); err != nil {
		t.Fatalf("failed to unmarshal content chunk: %v", err)
	}
	if contentChunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", contentChunk.Choices[0].Delta.Content)
	}

	// Verify last chunk has finish_reason.
	var lastChunk model.ChatStreamChunk
	if err := json.Unmarshal([]byte(sw.events[3]), &lastChunk); err != nil {
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

func TestAnthropic_StopReasonMapping(t *testing.T) {
	tests := []struct {
		anthropicReason string
		openaiReason    string
	}{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.anthropicReason, func(t *testing.T) {
			got := anthropicStopReason(tt.anthropicReason)
			if got != tt.openaiReason {
				t.Errorf("anthropicStopReason(%q) = %q, want %q", tt.anthropicReason, got, tt.openaiReason)
			}
		})
	}
}
