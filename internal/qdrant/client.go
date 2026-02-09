package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// CachedPayload is the data stored alongside each vector in Qdrant.
type CachedPayload struct {
	Response  *model.ChatResponse `json:"response"`
	Model     string              `json:"model"`
	CreatedAt int64               `json:"created_at"`
}

// SearchResult is a single match from Qdrant.
type SearchResult struct {
	ID      string         `json:"id"`
	Score   float32        `json:"score"`
	Payload *CachedPayload `json:"payload"`
}

// Client is a REST client for Qdrant.
type Client struct {
	baseURL    string
	apiKey     string
	collection string
	client     *http.Client
}

// NewClient creates a Qdrant REST client.
func NewClient(baseURL, apiKey, collection string) *Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		collection: collection,
		client:     &http.Client{Transport: transport},
	}
}

// EnsureCollection creates the collection if it doesn't exist.
func (c *Client) EnsureCollection(ctx context.Context, vectorSize int) error {
	body := map[string]any{
		"vectors": map[string]any{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return fmt.Errorf("marshaling collection config: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.baseURL+"/collections/"+c.collection, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return fmt.Errorf("creating collection request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("creating collection: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// 200 = created, 409 = already exists — both are fine.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("unexpected status creating collection: %d", resp.StatusCode)
	}
	return nil
}

type searchRequest struct {
	Vector      []float32    `json:"vector"`
	Limit       int          `json:"limit"`
	ScoreThresh float32      `json:"score_threshold"`
	WithPayload bool         `json:"with_payload"`
	Filter      *queryFilter `json:"filter,omitempty"`
}

type queryFilter struct {
	Must []filterCondition `json:"must"`
}

type filterCondition struct {
	Key   string       `json:"key"`
	Match *matchValue  `json:"match"`
}

type matchValue struct {
	Value string `json:"value"`
}

type searchResponse struct {
	Result []searchResultRaw `json:"result"`
}

type searchResultRaw struct {
	ID      string          `json:"id"`
	Score   float32         `json:"score"`
	Payload json.RawMessage `json:"payload"`
}

// Search finds similar vectors in the collection, filtered by model.
func (c *Client) Search(ctx context.Context, vector []float32, limit int, scoreThreshold float32, modelFilter string) ([]SearchResult, error) {
	body := searchRequest{
		Vector:      vector,
		Limit:       limit,
		ScoreThresh: scoreThreshold,
		WithPayload: true,
	}
	if modelFilter != "" {
		body.Filter = &queryFilter{
			Must: []filterCondition{
				{Key: "model", Match: &matchValue{Value: modelFilter}},
			},
		}
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("marshaling search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/collections/"+c.collection+"/points/search", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("creating search request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching qdrant: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("qdrant search error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decoding search response: %w", err)
	}

	results := make([]SearchResult, 0, len(sr.Result))
	for _, r := range sr.Result {
		var payload CachedPayload
		if err := json.Unmarshal(r.Payload, &payload); err != nil {
			continue
		}
		results = append(results, SearchResult{
			ID:      r.ID,
			Score:   r.Score,
			Payload: &payload,
		})
	}
	return results, nil
}

type upsertRequest struct {
	Points []point `json:"points"`
}

type point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload *CachedPayload `json:"payload"`
}

// Upsert inserts or updates a point in the collection.
func (c *Client) Upsert(ctx context.Context, id string, vector []float32, payload *CachedPayload) error {
	body := upsertRequest{
		Points: []point{
			{ID: id, Vector: vector, Payload: payload},
		},
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return fmt.Errorf("marshaling upsert request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.baseURL+"/collections/"+c.collection+"/points", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return fmt.Errorf("creating upsert request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("upserting to qdrant: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant upsert error (status %d)", resp.StatusCode)
	}
	return nil
}

// DeleteCollection deletes the collection from Qdrant.
func (c *Client) DeleteCollection(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/collections/"+c.collection, nil)
	if err != nil {
		return fmt.Errorf("creating delete collection request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("deleting collection: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status deleting collection: %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}
}
