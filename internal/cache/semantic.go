package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/embedding"
	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/qdrant"
)

// SemanticCache checks for semantically similar cached responses via embeddings + Qdrant.
type SemanticCache struct {
	embedder  *embedding.Client
	qdrant    *qdrant.Client
	threshold float32
}

// NewSemanticCache creates a new semantic cache.
func NewSemanticCache(embedder *embedding.Client, q *qdrant.Client, threshold float32) *SemanticCache {
	return &SemanticCache{
		embedder:  embedder,
		qdrant:    q,
		threshold: threshold,
	}
}

// Lookup embeds the request and searches Qdrant for a similar cached response.
// Returns (response, embedding, error). On any failure, returns (nil, nil, nil) for graceful fallthrough.
// The embedding is returned so Store() can reuse it without recomputing.
func (s *SemanticCache) Lookup(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, []float32, error) {
	text := embedding.TextFromMessages(req.Messages)

	emb, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return nil, nil, nil
	}

	results, err := s.qdrant.Search(ctx, emb, 1, s.threshold, req.Model)
	if err != nil {
		return nil, emb, nil
	}

	if len(results) > 0 && results[0].Payload != nil && results[0].Payload.Response != nil {
		return results[0].Payload.Response, emb, nil
	}

	return nil, emb, nil
}

// Store saves a response in Qdrant for future semantic lookups.
// If emb is non-nil it is reused; otherwise a fresh embedding is computed.
func (s *SemanticCache) Store(ctx context.Context, req *model.ChatRequest, resp *model.ChatResponse, emb []float32) error {
	if emb == nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		text := embedding.TextFromMessages(req.Messages)
		var err error
		emb, err = s.embedder.Embed(ctx, text)
		if err != nil {
			return fmt.Errorf("computing embedding for store: %w", err)
		}
	}

	id := pointID(req)
	payload := &qdrant.CachedPayload{
		Response:  resp,
		Model:     req.Model,
		CreatedAt: time.Now().Unix(),
	}

	return s.qdrant.Upsert(ctx, id, emb, payload)
}

// pointID generates a deterministic ID for a request based on its content hash.
func pointID(req *model.ChatRequest) string {
	text := embedding.TextFromMessages(req.Messages)
	h := sha256.Sum256([]byte(req.Model + ":" + text))
	return hex.EncodeToString(h[:16]) // 128-bit hex string
}
