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
// Returns (response, embedding, text, error). On any failure, returns (nil, nil, "", nil) for graceful fallthrough.
// The embedding and text are returned so Store() can reuse them without recomputing.
func (s *SemanticCache) Lookup(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, []float32, string, error) {
	text := embedding.TextFromMessages(req.Messages)

	emb, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return nil, nil, "", nil
	}

	results, err := s.qdrant.Search(ctx, emb, 1, s.threshold, req.Model)
	if err != nil {
		return nil, emb, text, nil
	}

	if len(results) > 0 && results[0].Payload != nil && results[0].Payload.Response != nil {
		return results[0].Payload.Response, emb, text, nil
	}

	return nil, emb, text, nil
}

// Store saves a response in Qdrant for future semantic lookups.
// If emb is non-nil it is reused; otherwise a fresh embedding is computed.
// If text is non-empty it is reused for the point ID; otherwise it is recomputed.
func (s *SemanticCache) Store(ctx context.Context, req *model.ChatRequest, resp *model.ChatResponse, emb []float32, text string) error {
	if text == "" {
		text = embedding.TextFromMessages(req.Messages)
	}

	if emb == nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		var err error
		emb, err = s.embedder.Embed(ctx, text)
		if err != nil {
			return fmt.Errorf("computing embedding for store: %w", err)
		}
	}

	id := pointIDFromText(req.Model, text)
	payload := &qdrant.CachedPayload{
		Response:  resp,
		Model:     req.Model,
		CreatedAt: time.Now().Unix(),
	}

	return s.qdrant.Upsert(ctx, id, emb, payload)
}

// pointIDFromText generates a deterministic ID from model and precomputed text.
func pointIDFromText(modelName, text string) string {
	h := sha256.New()
	h.Write([]byte(modelName))
	h.Write([]byte(":"))
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil)[:16]) // 128-bit hex string
}
