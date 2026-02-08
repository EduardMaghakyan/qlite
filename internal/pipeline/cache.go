package pipeline

import (
	"context"

	"github.com/eduardmaghakyan/qlite/internal/cache"
	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

// CacheStage checks the exact-match cache before dispatching to a provider.
// It implements both Stage and StreamStage.
type CacheStage struct {
	cache             *cache.ExactCache
	skipTempAboveZero bool
}

// NewCacheStage creates a new CacheStage.
// If skipTempAboveZero is true, requests with temperature explicitly > 0 bypass the cache.
func NewCacheStage(c *cache.ExactCache, skipTempAboveZero bool) *CacheStage {
	return &CacheStage{
		cache:             c,
		skipTempAboveZero: skipTempAboveZero,
	}
}

func (s *CacheStage) Name() string { return "cache" }

// Process handles non-streaming cache lookup.
// Returns nil to pass through to the next stage on miss.
func (s *CacheStage) Process(ctx context.Context, req *model.ProxyRequest) (*model.ProxyResponse, error) {
	if s.shouldSkip(req) {
		return nil, nil
	}

	entry, ok := s.cache.Get(&req.ChatRequest)
	if !ok {
		return nil, nil
	}

	return &model.ProxyResponse{
		ChatResponse: entry.Response,
		OutputTokens: entry.Response.Usage.CompletionTokens,
		Cost:         0,
		CacheStatus:  "HIT",
		ProviderName: "cache",
	}, nil
}

// ProcessStream handles streaming cache lookup.
// On hit, replays the cached response as SSE events.
func (s *CacheStage) ProcessStream(ctx context.Context, req *model.ProxyRequest, sw sse.Writer) (*model.ProxyResponse, error) {
	if s.shouldSkip(req) {
		return nil, nil
	}

	entry, ok := s.cache.Get(&req.ChatRequest)
	if !ok {
		return nil, nil
	}

	sw.SetHeader("X-Cache", "HIT")
	sw.SetHeader("X-Provider", "cache")

	if err := sse.WriteResponseAsSSE(sw, entry.Response); err != nil {
		return nil, err
	}

	return &model.ProxyResponse{
		ChatResponse: entry.Response,
		OutputTokens: entry.Response.Usage.CompletionTokens,
		Cost:         0,
		CacheStatus:  "HIT",
		ProviderName: "cache",
	}, nil
}

// shouldSkip returns true if this request should bypass the cache.
func (s *CacheStage) shouldSkip(req *model.ProxyRequest) bool {
	if !s.skipTempAboveZero {
		return false
	}
	// Only skip when temperature is explicitly set to > 0.
	return req.ChatRequest.Temperature != nil && *req.ChatRequest.Temperature > 0
}
