package cache

import (
	"container/list"
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

// lruEntry wraps an Entry with its cache key for O(1) eviction.
type lruEntry struct {
	key   string
	entry *Entry
}

// ExactCache is an in-memory LRU cache keyed by SHA-256 of (model, messages, temperature, top_p).
type ExactCache struct {
	mu         sync.RWMutex
	items      map[string]*list.Element
	order      *list.List // front = most recently used, back = least recently used
	ttl        time.Duration
	maxEntries int
}

// New creates a new ExactCache with the given TTL and max entry count.
func New(ttl time.Duration, maxEntries int) *ExactCache {
	return &ExactCache{
		items:      make(map[string]*list.Element),
		order:      list.New(),
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

// KeyFor computes a SHA-256 hex string from the cache-relevant fields of a request.
func KeyFor(req *model.ChatRequest) string {
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
	return c.GetByKey(KeyFor(req))
}

// GetByKey looks up a cached response by precomputed key. Returns nil if not found or expired.
func (c *ExactCache) GetByKey(key string) (*Entry, bool) {
	c.mu.Lock()
	elem, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		return nil, false
	}

	le := elem.Value.(*lruEntry)
	if time.Now().After(le.entry.ExpiresAt) {
		// Expired — remove under write lock.
		c.order.Remove(elem)
		delete(c.items, key)
		c.mu.Unlock()
		return nil, false
	}

	// Move to front (most recently used).
	c.order.MoveToFront(elem)
	entry := le.entry
	c.mu.Unlock()
	return entry, true
}

// Put stores a response in the cache. If at capacity, the least recently used entry is evicted.
func (c *ExactCache) Put(req *model.ChatRequest, resp *model.ChatResponse) {
	c.PutByKey(KeyFor(req), resp)
}

// PutByKey stores a response using a precomputed key.
func (c *ExactCache) PutByKey(key string, resp *model.ChatResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := &Entry{
		Response:  resp,
		ExpiresAt: time.Now().Add(c.ttl),
	}

	if elem, ok := c.items[key]; ok {
		// Update existing entry, move to front.
		elem.Value.(*lruEntry).entry = entry
		c.order.MoveToFront(elem)
		return
	}

	// Evict LRU if at capacity.
	if c.order.Len() >= c.maxEntries {
		c.evictLRU()
	}

	le := &lruEntry{key: key, entry: entry}
	elem := c.order.PushFront(le)
	c.items[key] = elem
}

// Clear removes all entries from the cache.
func (c *ExactCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.order.Init()
}

// Len returns the current number of entries in the cache.
func (c *ExactCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.order.Len()
}

// evictLRU removes the least recently used entry. Must be called under write lock.
func (c *ExactCache) evictLRU() {
	back := c.order.Back()
	if back == nil {
		return
	}
	le := back.Value.(*lruEntry)
	c.order.Remove(back)
	delete(c.items, le.key)
}
