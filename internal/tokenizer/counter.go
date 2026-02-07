package tokenizer

import (
	"strings"
	"sync"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/pkoukk/tiktoken-go"
)

// Counter provides token counting for chat messages.
type Counter struct {
	mu        sync.RWMutex
	encodings map[string]*tiktoken.Tiktoken
}

// NewCounter creates a new token counter.
func NewCounter() *Counter {
	return &Counter{
		encodings: make(map[string]*tiktoken.Tiktoken),
	}
}

// modelEncoding maps model prefixes to tiktoken encoding names.
var modelEncoding = map[string]string{
	"gpt-4o":    "o200k_base",
	"gpt-4.1":   "o200k_base",
	"o1":        "o200k_base",
	"o3":        "o200k_base",
}

func encodingForModel(modelName string) string {
	for prefix, enc := range modelEncoding {
		if strings.HasPrefix(modelName, prefix) {
			return enc
		}
	}
	return ""
}

func (c *Counter) getEncoding(modelName string) *tiktoken.Tiktoken {
	encName := encodingForModel(modelName)
	if encName == "" {
		return nil
	}

	c.mu.RLock()
	enc, ok := c.encodings[encName]
	c.mu.RUnlock()
	if ok {
		return enc
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock.
	if enc, ok := c.encodings[encName]; ok {
		return enc
	}

	enc, err := tiktoken.GetEncoding(encName)
	if err != nil {
		return nil
	}
	c.encodings[encName] = enc
	return enc
}

// CountMessages estimates the token count for a slice of messages.
// Uses tiktoken when available, falls back to len(text)/4.
func (c *Counter) CountMessages(modelName string, messages []model.Message) int {
	enc := c.getEncoding(modelName)
	if enc == nil {
		return c.fallbackCount(messages)
	}

	// OpenAI token counting: each message has overhead tokens.
	// See: https://platform.openai.com/docs/guides/chat/introduction
	tokensPerMessage := 3 // every message follows <|im_start|>{role}\n{content}<|im_end|>\n
	tokens := 0
	for _, msg := range messages {
		tokens += tokensPerMessage
		tokens += len(enc.Encode(msg.Role, nil, nil))
		tokens += len(enc.Encode(msg.Content, nil, nil))
	}
	tokens += 3 // every reply is primed with <|im_start|>assistant<|message|>
	return tokens
}

// CountText estimates the token count for a single text string.
func (c *Counter) CountText(modelName string, text string) int {
	enc := c.getEncoding(modelName)
	if enc == nil {
		return len(text) / 4
	}
	return len(enc.Encode(text, nil, nil))
}

// QuickEstimate returns a fast token estimate using len/4 heuristic (no tiktoken).
func (c *Counter) QuickEstimate(messages []model.Message) int {
	return c.fallbackCount(messages)
}

func (c *Counter) fallbackCount(messages []model.Message) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) / 4
	}
	return total
}
