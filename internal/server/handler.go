package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/cache"
	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/pipeline"
	"github.com/eduardmaghakyan/qlite/internal/pricing"
	"github.com/eduardmaghakyan/qlite/internal/sse"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

// Handler serves the /v1/chat/completions endpoint.
type Handler struct {
	pipeline *pipeline.Pipeline
	counter  *tokenizer.Counter
	logger   *slog.Logger
	cache    *cache.ExactCache
}

// NewHandler creates a new request handler. The cache parameter may be nil (disabled).
func NewHandler(p *pipeline.Pipeline, counter *tokenizer.Counter, logger *slog.Logger, c *cache.ExactCache) *Handler {
	return &Handler{
		pipeline: p,
		counter:  counter,
		logger:   logger,
		cache:    c,
	}
}

// RegisterRoutes registers all HTTP routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", h.handleChatCompletions)
	mux.HandleFunc("GET /health", h.handleHealth)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var chatReq model.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&chatReq); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body: "+err.Error())
		return
	}

	if chatReq.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	apiKey := extractAPIKey(r)

	// For non-streaming, skip local token counting — upstream returns accurate Usage.
	// For streaming, use fast len/4 heuristic to set the X-Tokens-Input header.
	var inputTokens int
	if chatReq.Stream {
		inputTokens = h.counter.QuickEstimate(chatReq.Messages)
	}

	proxyReq := &model.ProxyRequest{
		ChatRequest: chatReq,
		RequestID:   GetRequestID(r.Context()),
		ReceivedAt:  time.Now(),
		InputTokens: inputTokens,
		APIKey:      apiKey,
	}

	if chatReq.Stream {
		h.handleStreaming(w, r, proxyReq)
	} else {
		h.handleNonStreaming(w, r, proxyReq)
	}
}

func (h *Handler) handleNonStreaming(w http.ResponseWriter, r *http.Request, proxyReq *model.ProxyRequest) {
	resp, err := h.pipeline.Execute(r.Context(), proxyReq)
	if err != nil {
		h.logger.Error("pipeline error", "error", err, "request_id", proxyReq.RequestID)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	// Store in cache on miss.
	if h.cache != nil && resp.CacheStatus == "MISS" {
		h.cache.Put(&proxyReq.ChatRequest, resp.ChatResponse)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Cost", strconv.FormatFloat(resp.Cost, 'f', 8, 64))
	w.Header().Set("X-Tokens-Input", strconv.Itoa(resp.ChatResponse.Usage.PromptTokens))
	w.Header().Set("X-Tokens-Output", strconv.Itoa(resp.OutputTokens))
	w.Header().Set("X-Cache", resp.CacheStatus)
	w.Header().Set("X-Provider", resp.ProviderName)

	if resp.CacheStatus == "HIT" {
		totalTokens := resp.ChatResponse.Usage.PromptTokens + resp.ChatResponse.Usage.CompletionTokens
		w.Header().Set("X-Tokens-Saved", strconv.Itoa(totalTokens))
		costSaved := pricing.Calculate(proxyReq.ChatRequest.Model, resp.ChatResponse.Usage.PromptTokens, resp.ChatResponse.Usage.CompletionTokens)
		w.Header().Set("X-Cost-Saved", strconv.FormatFloat(costSaved, 'f', 8, 64))
	}

	if err := json.NewEncoder(w).Encode(resp.ChatResponse); err != nil {
		h.logger.Error("failed to write response", "error", err, "request_id", proxyReq.RequestID)
	}
}

func (h *Handler) handleStreaming(w http.ResponseWriter, r *http.Request, proxyReq *model.ProxyRequest) {
	sw := sse.NewWriter(w)
	sw.SetHeader("X-Tokens-Input", strconv.Itoa(proxyReq.InputTokens))
	sw.SetHeader("X-Cache", "MISS")

	resp, err := h.pipeline.ExecuteStream(r.Context(), proxyReq, sw)
	if err != nil {
		h.logger.Error("streaming pipeline error", "error", err, "request_id", proxyReq.RequestID)
		// For streaming, we can't write an error response if we've already started streaming.
		// The error will manifest as an incomplete stream to the client.
		return
	}

	if resp != nil {
		h.logger.Info("stream completed",
			"request_id", proxyReq.RequestID,
			"output_tokens", resp.OutputTokens,
			"cost", resp.Cost,
			"provider", resp.ProviderName,
		)
	}
}

func extractAPIKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.ErrorResponse{
		Error: model.ErrorDetail{
			Message: message,
			Type:    errType,
		},
	})
}
