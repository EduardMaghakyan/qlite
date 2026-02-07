package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

// Entry holds a cached response with its expiration time.
type Entry struct {
	Response  *model.ChatResponse
	ExpiresAt time.Time
}

// ExactCache is an in-memory cache keyed by SHA-256 of (model, messages, temperature, top_p).
type ExactCache struct {
	mu         sync.RWMutex
	entries    map[string]*Entry
	ttl        time.Duration
	maxEntries int
}

// New creates a new ExactCache with the given TTL and max entry count.
func New(ttl time.Duration, maxEntries int) *ExactCache {
	return &ExactCache{
		entries:    make(map[string]*Entry),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

// cacheKey is the canonical structure hashed for the cache key.
type cacheKey struct {
	Model       string          `json:"model"`
	Messages    []model.Message `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
}

// keyFor computes a SHA-256 hex string from the cache-relevant fields of a request.
func keyFor(req *model.ChatRequest) string {
	k := cacheKey{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	data, _ := json.Marshal(k)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Get looks up a cached response. Returns nil if not found or expired.
func (c *ExactCache) Get(req *model.ChatRequest) (*Entry, bool) {
	key := keyFor(req)

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if time.Now().After(entry.ExpiresAt) {
		// Lazy eviction of expired entry.
		c.mu.Lock()
		// Double-check under write lock.
		if e, ok := c.entries[key]; ok && time.Now().After(e.ExpiresAt) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}

	return entry, true
}

// Put stores a response in the cache. If at capacity, the oldest entry is evicted.
func (c *ExactCache) Put(req *model.ChatRequest, resp *model.ChatResponse) {
	key := keyFor(req)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict oldest if at capacity and this is a new key.
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxEntries {
		c.evictOldest()
	}

	c.entries[key] = &Entry{
		Response:  resp,
		ExpiresAt: time.Now().Add(c.ttl),
	}
}

// Len returns the current number of entries in the cache.
func (c *ExactCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// evictOldest removes the entry with the earliest ExpiresAt. Must be called under write lock.
func (c *ExactCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for k, e := range c.entries {
		if oldestKey == "" || e.ExpiresAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.ExpiresAt
		}
	}

	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
