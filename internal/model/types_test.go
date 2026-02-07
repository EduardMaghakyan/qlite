package model

import (
	"encoding/json"
	"testing"
)

func TestChatRequest_JSONRoundtrip(t *testing.T) {
	temp := 0.7
	req := ChatRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello!"},
		},
		Temperature: &temp,
		Stream:      false,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ChatRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Model != req.Model {
		t.Errorf("expected model %s, got %s", req.Model, decoded.Model)
	}
	if len(decoded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(decoded.Messages))
	}
	if decoded.Temperature == nil || *decoded.Temperature != 0.7 {
		t.Error("expected temperature 0.7")
	}
}

func TestChatResponse_JSONRoundtrip(t *testing.T) {
	resp := ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "Hi!"},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.ID != resp.ID {
		t.Errorf("expected ID %s, got %s", resp.ID, decoded.ID)
	}
	if decoded.Usage.TotalTokens != 15 {
		t.Errorf("expected 15 total tokens, got %d", decoded.Usage.TotalTokens)
	}
}

func TestChatStreamChunk_JSONRoundtrip(t *testing.T) {
	usage := Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	chunk := ChatStreamChunk{
		ID:      "chatcmpl-123",
		Object:  "chat.completion.chunk",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []StreamChoice{
			{
				Index:        0,
				Delta:        Delta{Content: "Hello"},
				FinishReason: "",
			},
		},
		Usage: &usage,
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ChatStreamChunk
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", decoded.Choices[0].Delta.Content)
	}
	if decoded.Usage == nil || decoded.Usage.PromptTokens != 10 {
		t.Error("expected usage with 10 prompt tokens")
	}
}

func TestChatStreamChunk_NilUsage(t *testing.T) {
	chunk := ChatStreamChunk{
		ID:     "chatcmpl-123",
		Object: "chat.completion.chunk",
		Model:  "gpt-4o",
		Choices: []StreamChoice{
			{Index: 0, Delta: Delta{Content: "Hi"}},
		},
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// usage should be omitted.
	var raw map[string]any
	json.Unmarshal(data, &raw)
	if _, exists := raw["usage"]; exists {
		t.Error("expected usage to be omitted when nil")
	}
}

func TestErrorResponse_JSON(t *testing.T) {
	errResp := ErrorResponse{
		Error: ErrorDetail{
			Message: "model not found",
			Type:    "invalid_request_error",
			Code:    "model_not_found",
		},
	}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ErrorResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.Error.Message != "model not found" {
		t.Errorf("expected 'model not found', got %q", decoded.Error.Message)
	}
}
