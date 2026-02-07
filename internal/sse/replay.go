package sse

import (
	"encoding/json"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
)

// WriteResponseAsSSE replays a complete ChatResponse as SSE events.
// This is used for serving cached responses to streaming requests.
func WriteResponseAsSSE(sw Writer, resp *model.ChatResponse) error {
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
	data, err := json.Marshal(roleChunk)
	if err != nil {
		return err
	}
	if err := sw.WriteEvent(data); err != nil {
		return err
	}

	// Send content chunk(s).
	for _, choice := range resp.Choices {
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
		data, err := json.Marshal(contentChunk)
		if err != nil {
			return err
		}
		if err := sw.WriteEvent(data); err != nil {
			return err
		}
	}

	// Send finish chunk with usage.
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
	data, err = json.Marshal(finishChunk)
	if err != nil {
		return err
	}
	if err := sw.WriteEvent(data); err != nil {
		return err
	}

	return sw.Done()
}
