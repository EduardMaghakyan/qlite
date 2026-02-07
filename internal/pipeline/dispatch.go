package pipeline

import (
	"context"
	"fmt"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/pricing"
	"github.com/eduardmaghakyan/qlite/internal/provider"
	"github.com/eduardmaghakyan/qlite/internal/sse"
	"github.com/eduardmaghakyan/qlite/internal/tokenizer"
)

// DispatchStage routes requests to the appropriate provider.
type DispatchStage struct {
	registry *provider.Registry
	counter  *tokenizer.Counter
}

// NewDispatchStage creates a new provider dispatch stage.
func NewDispatchStage(registry *provider.Registry, counter *tokenizer.Counter) *DispatchStage {
	return &DispatchStage{
		registry: registry,
		counter:  counter,
	}
}

func (d *DispatchStage) Name() string { return "dispatch" }

// Process handles non-streaming requests.
func (d *DispatchStage) Process(ctx context.Context, req *model.ProxyRequest) (*model.ProxyResponse, error) {
	p, err := d.registry.Lookup(req.ChatRequest.Model)
	if err != nil {
		return nil, fmt.Errorf("looking up provider: %w", err)
	}

	chatResp, err := p.Chat(ctx, &req.ChatRequest)
	if err != nil {
		return nil, fmt.Errorf("calling provider %s: %w", p.Name(), err)
	}

	outputTokens := chatResp.Usage.CompletionTokens
	cost := pricing.Calculate(req.ChatRequest.Model, chatResp.Usage.PromptTokens, outputTokens)

	return &model.ProxyResponse{
		ChatResponse: chatResp,
		OutputTokens: outputTokens,
		Cost:         cost,
		CacheStatus:  "MISS",
		ProviderName: p.Name(),
	}, nil
}

// ProcessStream handles streaming requests.
func (d *DispatchStage) ProcessStream(ctx context.Context, req *model.ProxyRequest, sw sse.Writer) (*model.ProxyResponse, error) {
	p, err := d.registry.Lookup(req.ChatRequest.Model)
	if err != nil {
		return nil, fmt.Errorf("looking up provider: %w", err)
	}

	usage, err := p.ChatStream(ctx, &req.ChatRequest, sw)
	if err != nil {
		return nil, fmt.Errorf("streaming from provider %s: %w", p.Name(), err)
	}

	var outputTokens int
	var inputTokens int
	if usage != nil {
		outputTokens = usage.CompletionTokens
		inputTokens = usage.PromptTokens
	}

	cost := pricing.Calculate(req.ChatRequest.Model, inputTokens, outputTokens)

	return &model.ProxyResponse{
		OutputTokens: outputTokens,
		Cost:         cost,
		CacheStatus:  "MISS",
		ProviderName: p.Name(),
	}, nil
}
