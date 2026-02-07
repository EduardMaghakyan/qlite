package pipeline

import (
	"context"
	"fmt"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

// Stage processes a proxy request and returns a response.
// Returning a non-nil ProxyResponse short-circuits the pipeline.
type Stage interface {
	Name() string
	Process(ctx context.Context, req *model.ProxyRequest) (*model.ProxyResponse, error)
}

// StreamStage processes a streaming proxy request.
type StreamStage interface {
	Name() string
	ProcessStream(ctx context.Context, req *model.ProxyRequest, sw sse.Writer) (*model.ProxyResponse, error)
}

// Pipeline holds an ordered list of stages.
type Pipeline struct {
	stages []any // each is Stage and/or StreamStage
}

// New creates a pipeline from the given stages.
// Each stage must implement Stage, StreamStage, or both.
func New(stages ...any) (*Pipeline, error) {
	for i, s := range stages {
		_, isStage := s.(Stage)
		_, isStream := s.(StreamStage)
		if !isStage && !isStream {
			return nil, fmt.Errorf("stage %d does not implement Stage or StreamStage", i)
		}
	}
	return &Pipeline{stages: stages}, nil
}

// Execute runs the non-streaming pipeline.
func (p *Pipeline) Execute(ctx context.Context, req *model.ProxyRequest) (*model.ProxyResponse, error) {
	for _, s := range p.stages {
		stage, ok := s.(Stage)
		if !ok {
			continue
		}
		resp, err := stage.Process(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("stage %s: %w", stage.Name(), err)
		}
		if resp != nil {
			return resp, nil
		}
	}
	return nil, fmt.Errorf("pipeline completed without producing a response")
}

// ExecuteStream runs the streaming pipeline.
func (p *Pipeline) ExecuteStream(ctx context.Context, req *model.ProxyRequest, sw sse.Writer) (*model.ProxyResponse, error) {
	for _, s := range p.stages {
		stage, ok := s.(StreamStage)
		if !ok {
			continue
		}
		resp, err := stage.ProcessStream(ctx, req, sw)
		if err != nil {
			return nil, fmt.Errorf("stage %s: %w", stage.Name(), err)
		}
		if resp != nil {
			return resp, nil
		}
	}
	return nil, fmt.Errorf("streaming pipeline completed without producing a response")
}
