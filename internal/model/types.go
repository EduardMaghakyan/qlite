package model

import (
	"encoding/json"
	"time"
)

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest mirrors the OpenAI chat completions request.
type ChatRequest struct {
	Model            string          `json:"model"`
	Messages         []Message       `json:"messages"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	N                *int            `json:"n,omitempty"`
	Stream           bool            `json:"stream"`
	StreamOptions    *StreamOptions  `json:"stream_options,omitempty"`
	Stop             json.RawMessage `json:"stop,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	User             string          `json:"user,omitempty"`
}

// StreamOptions controls streaming behavior.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Usage represents token usage information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice represents a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse mirrors the OpenAI chat completions response.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Delta represents incremental content in a streaming chunk.
type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// StreamChoice represents a choice in a streaming chunk.
type StreamChoice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// ChatStreamChunk mirrors an OpenAI streaming chunk.
type ChatStreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// ProxyRequest wraps a ChatRequest with proxy-specific metadata.
type ProxyRequest struct {
	ChatRequest    ChatRequest
	RequestID      string
	ReceivedAt     time.Time
	InputTokens    int
	APIKey         string
	CacheKey       string // precomputed exact-cache key, set by CacheStage
}

// ProxyResponse wraps a ChatResponse with proxy-specific metadata.
type ProxyResponse struct {
	ChatResponse *ChatResponse
	OutputTokens int
	Cost         float64
	CacheStatus  string
	ProviderName string
}

// ErrorResponse represents an OpenAI-compatible error.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail holds error information.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}
