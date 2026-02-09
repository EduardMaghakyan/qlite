package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

// Anthropic SSE event type byte slices for zero-alloc comparison.
var (
	eventMessageStart     = []byte("message_start")
	eventContentBlockDelta = []byte("content_block_delta")
	eventMessageDelta     = []byte("message_delta")
	eventMessageStop      = []byte("message_stop")
)

// Anthropic is a provider that speaks the Anthropic Messages API.
type Anthropic struct {
	name    string
	baseURL string
	apiKey  string
	models  []string
	client  *http.Client
}

// NewAnthropic creates a new Anthropic provider.
func NewAnthropic(name, baseURL, apiKey string, models []string) *Anthropic {
	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 1000,
		IdleConnTimeout:     90 * time.Second,
		WriteBufferSize:     32 << 10,
		ReadBufferSize:      32 << 10,
		ForceAttemptHTTP2:   true,
	}
	return &Anthropic{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		models:  models,
		client:  &http.Client{Transport: transport},
	}
}

func (a *Anthropic) Name() string    { return a.name }
func (a *Anthropic) Models() []string { return a.models }

// anthropicRequest is the Anthropic Messages API request format.
type anthropicRequest struct {
	Model       string            `json:"model"`
	Messages    []anthropicMsg    `json:"messages"`
	System      string            `json:"system,omitempty"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature *float64          `json:"temperature,omitempty"`
	TopP        *float64          `json:"top_p,omitempty"`
	Stream      bool              `json:"stream,omitempty"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse is the Anthropic Messages API response format.
type anthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []anthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	Usage        anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Anthropic SSE event types.
type anthropicMessageStart struct {
	Type    string            `json:"type"`
	Message anthropicResponse `json:"message"`
}

type anthropicContentBlockDelta struct {
	Type  string                `json:"type"`
	Index int                   `json:"index"`
	Delta anthropicDeltaContent `json:"delta"`
}

type anthropicDeltaContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessageDelta struct {
	Type  string                     `json:"type"`
	Delta anthropicMessageDeltaInner `json:"delta"`
	Usage anthropicDeltaUsage        `json:"usage"`
}

type anthropicMessageDeltaInner struct {
	StopReason string `json:"stop_reason"`
}

type anthropicDeltaUsage struct {
	OutputTokens int `json:"output_tokens"`
}

func (a *Anthropic) convertRequest(req *model.ChatRequest) *anthropicRequest {
	ar := &anthropicRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	if req.MaxTokens != nil {
		ar.MaxTokens = *req.MaxTokens
	} else {
		ar.MaxTokens = 4096
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			ar.System = msg.Content
			continue
		}
		ar.Messages = append(ar.Messages, anthropicMsg{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return ar
}

func anthropicStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return reason
	}
}

// Chat sends a non-streaming chat completion request.
func (a *Anthropic) Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	ar := a.convertRequest(req)
	ar.Stream = false

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(ar); err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/messages", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var ar2 anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar2); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// Concatenate content blocks.
	var content strings.Builder
	for _, c := range ar2.Content {
		if c.Type == "text" {
			content.WriteString(c.Text)
		}
	}

	totalTokens := ar2.Usage.InputTokens + ar2.Usage.OutputTokens
	return &model.ChatResponse{
		ID:      ar2.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   ar2.Model,
		Choices: []model.Choice{
			{
				Index: 0,
				Message: model.Message{
					Role:    "assistant",
					Content: content.String(),
				},
				FinishReason: anthropicStopReason(ar2.StopReason),
			},
		},
		Usage: model.Usage{
			PromptTokens:     ar2.Usage.InputTokens,
			CompletionTokens: ar2.Usage.OutputTokens,
			TotalTokens:      totalTokens,
		},
	}, nil
}

// ChatStream sends a streaming chat completion request and relays SSE chunks.
func (a *Anthropic) ChatStream(ctx context.Context, req *model.ChatRequest, sw sse.Writer) (*model.Usage, error) {
	ar := a.convertRequest(req)
	ar.Stream = true

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(ar); err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/messages", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var usage model.Usage
	var msgID string
	var modelName string
	var curEvent []byte

	eventPrefix := []byte("event: ")

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()

		// Parse event type lines.
		if bytes.HasPrefix(line, eventPrefix) {
			curEvent = line[7:]
			continue
		}

		// Parse data lines.
		if !bytes.HasPrefix(line, dataPrefix) {
			continue
		}
		data := line[len(dataPrefix):]

		if bytes.Equal(curEvent, eventMessageStart) {
			var ms anthropicMessageStart
			if err := json.Unmarshal(data, &ms); err != nil {
				continue
			}
			msgID = ms.Message.ID
			modelName = ms.Message.Model
			usage.PromptTokens = ms.Message.Usage.InputTokens

			chunk := model.ChatStreamChunk{
				ID:      msgID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []model.StreamChoice{
					{Index: 0, Delta: model.Delta{Role: "assistant"}},
				},
			}
			if err := sse.WriteJSON(sw, chunk); err != nil {
				return &usage, fmt.Errorf("writing event: %w", err)
			}
		} else if bytes.Equal(curEvent, eventContentBlockDelta) {
			var cbd anthropicContentBlockDelta
			if err := json.Unmarshal(data, &cbd); err != nil {
				continue
			}

			chunk := model.ChatStreamChunk{
				ID:      msgID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []model.StreamChoice{
					{Index: 0, Delta: model.Delta{Content: cbd.Delta.Text}},
				},
			}
			if err := sse.WriteJSON(sw, chunk); err != nil {
				return &usage, fmt.Errorf("writing event: %w", err)
			}
		} else if bytes.Equal(curEvent, eventMessageDelta) {
			var md anthropicMessageDelta
			if err := json.Unmarshal(data, &md); err != nil {
				continue
			}
			usage.CompletionTokens = md.Usage.OutputTokens
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

			chunk := model.ChatStreamChunk{
				ID:      msgID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []model.StreamChoice{
					{
						Index:        0,
						Delta:        model.Delta{},
						FinishReason: anthropicStopReason(md.Delta.StopReason),
					},
				},
			}
			if err := sse.WriteJSON(sw, chunk); err != nil {
				return &usage, fmt.Errorf("writing event: %w", err)
			}
		} else if bytes.Equal(curEvent, eventMessageStop) {
			if err := sw.Done(); err != nil {
				return &usage, fmt.Errorf("writing done: %w", err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return &usage, fmt.Errorf("reading stream: %w", err)
	}

	return &usage, nil
}

func (a *Anthropic) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}
