package sse

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

var replayBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// WriteResponseAsSSE replays a complete ChatResponse as SSE events.
// This is used for serving cached responses to streaming requests.
func WriteResponseAsSSE(sw Writer, resp *model.ChatResponse) error {
	buf := replayBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer replayBufPool.Put(buf)

	// Send role chunk.
	roleChunk := model.ChatStreamChunk{
		ID:      resp.ID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []model.StreamChoice{
			{
				Index: 0,
				Delta: model.Delta{Role: "assistant"},
			},
		},
	}
	if err := json.NewEncoder(buf).Encode(roleChunk); err != nil {
		return err
	}
	if err := sw.WriteEvent(buf.Bytes()); err != nil {
		return err
	}

	// Send content chunk(s).
	for _, choice := range resp.Choices {
		buf.Reset()
		contentChunk := model.ChatStreamChunk{
			ID:      resp.ID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resp.Model,
			Choices: []model.StreamChoice{
				{
					Index: choice.Index,
					Delta: model.Delta{Content: choice.Message.Content},
				},
			},
		}
		if err := json.NewEncoder(buf).Encode(contentChunk); err != nil {
			return err
		}
		if err := sw.WriteEvent(buf.Bytes()); err != nil {
			return err
		}
	}

	// Send finish chunk with usage.
	buf.Reset()
	finishChunk := model.ChatStreamChunk{
		ID:      resp.ID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []model.StreamChoice{
			{
				Index:        0,
				Delta:        model.Delta{},
				FinishReason: "stop",
			},
		},
		Usage: &resp.Usage,
	}
	if err := json.NewEncoder(buf).Encode(finishChunk); err != nil {
		return err
	}
	if err := sw.WriteEvent(buf.Bytes()); err != nil {
		return err
	}

	return sw.Done()
}
