package provider

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

// Provider defines the interface for LLM API providers.
type Provider interface {
	// Name returns the provider's identifier.
	Name() string
	// Models returns the list of models this provider supports.
	Models() []string
	// Chat sends a non-streaming chat completion request.
	Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error)
	// ChatStream sends a streaming chat completion request,
	// writing SSE chunks to the provided writer.
	ChatStream(ctx context.Context, req *model.ChatRequest, sw sse.Writer) (*model.Usage, error)
}

// Registry maps model names to providers.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	frozen    atomic.Pointer[map[string]Provider]
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider for all its supported models.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range p.Models() {
		r.providers[m] = p
	}
}

// Freeze creates an immutable snapshot for lock-free reads.
// Call after all providers are registered.
func (r *Registry) Freeze() {
	r.mu.RLock()
	snapshot := make(map[string]Provider, len(r.providers))
	for k, v := range r.providers {
		snapshot[k] = v
	}
	r.mu.RUnlock()
	r.frozen.Store(&snapshot)
}

// Lookup returns the provider for a given model name.
func (r *Registry) Lookup(model string) (Provider, error) {
	if m := r.frozen.Load(); m != nil {
		p, ok := (*m)[model]
		if !ok {
			return nil, fmt.Errorf("no provider registered for model %q", model)
		}
		return p, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[model]
	if !ok {
		return nil, fmt.Errorf("no provider registered for model %q", model)
	}
	return p, nil
}
