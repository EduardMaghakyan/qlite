package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/eduardmaghakyan/qlite/internal/model"
	"github.com/eduardmaghakyan/qlite/internal/sse"
)

// Google is a provider that speaks the Gemini API.
type Google struct {
	name    string
	baseURL string
	apiKey  string
	models  []string
	client  *http.Client
}

// NewGoogle creates a new Google (Gemini) provider.
func NewGoogle(name, baseURL, apiKey string, models []string) *Google {
	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 1000,
		IdleConnTimeout:     90 * time.Second,
		WriteBufferSize:     32 << 10,
		ReadBufferSize:      32 << 10,
		ForceAttemptHTTP2:   true,
	}
	return &Google{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		models:  models,
		client:  &http.Client{Transport: transport},
	}
}

func (g *Google) Name() string    { return g.name }
func (g *Google) Models() []string { return g.models }

// Gemini request types.
type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

// Gemini response types.
type geminiResponse struct {
	Candidates    []geminiCandidate  `json:"candidates"`
	UsageMetadata *geminiUsage       `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func (g *Google) convertRequest(req *model.ChatRequest) *geminiRequest {
	gr := &geminiRequest{}

	var genConfig geminiGenerationConfig
	hasConfig := false
	if req.Temperature != nil {
		genConfig.Temperature = req.Temperature
		hasConfig = true
	}
	if req.TopP != nil {
		genConfig.TopP = req.TopP
		hasConfig = true
	}
	if req.MaxTokens != nil {
		genConfig.MaxOutputTokens = req.MaxTokens
		hasConfig = true
	}
	if hasConfig {
		gr.GenerationConfig = &genConfig
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			gr.SystemInstruction = &geminiSystemInstruction{
				Parts: []geminiPart{{Text: msg.Content}},
			}
			continue
		}

		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		gr.Contents = append(gr.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: msg.Content}},
		})
	}

	return gr
}

func (g *Google) chatURL(modelName string) string {
	return g.baseURL + "/models/" + modelName + ":generateContent?key=" + g.apiKey
}

func (g *Google) streamURL(modelName string) string {
	return g.baseURL + "/models/" + modelName + ":streamGenerateContent?alt=sse&key=" + g.apiKey
}

func geminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		return reason
	}
}

// Chat sends a non-streaming chat completion request.
func (g *Google) Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	gr := g.convertRequest(req)

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(gr); err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.chatURL(req.Model), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var gr2 geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr2); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var content string
	var finishReason string
	if len(gr2.Candidates) > 0 {
		cand := gr2.Candidates[0]
		if len(cand.Content.Parts) > 0 {
			content = cand.Content.Parts[0].Text
		}
		finishReason = geminiFinishReason(cand.FinishReason)
	}

	var usage model.Usage
	if gr2.UsageMetadata != nil {
		usage = model.Usage{
			PromptTokens:     gr2.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr2.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr2.UsageMetadata.TotalTokenCount,
		}
	}

	return &model.ChatResponse{
		ID:      fmt.Sprintf("gen-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []model.Choice{
			{
				Index: 0,
				Message: model.Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: finishReason,
			},
		},
		Usage: usage,
	}, nil
}

// ChatStream sends a streaming chat completion request and relays SSE chunks.
func (g *Google) ChatStream(ctx context.Context, req *model.ChatRequest, sw sse.Writer) (*model.Usage, error) {
	gr := g.convertRequest(req)

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := json.NewEncoder(buf).Encode(gr); err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.streamURL(req.Model), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	genID := fmt.Sprintf("gen-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	var usage model.Usage
	first := true

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, dataPrefix) {
			continue
		}
		data := line[len(dataPrefix):]

		var gr2 geminiResponse
		if err := json.Unmarshal(data, &gr2); err != nil {
			continue
		}

		// Track usage from each chunk.
		if gr2.UsageMetadata != nil {
			usage = model.Usage{
				PromptTokens:     gr2.UsageMetadata.PromptTokenCount,
				CompletionTokens: gr2.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      gr2.UsageMetadata.TotalTokenCount,
			}
		}

		// Emit role chunk on first event.
		if first {
			first = false
			roleChunk := model.ChatStreamChunk{
				ID:      genID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   req.Model,
				Choices: []model.StreamChoice{
					{Index: 0, Delta: model.Delta{Role: "assistant"}},
				},
			}
			if err := sse.WriteJSON(sw, roleChunk); err != nil {
				return &usage, fmt.Errorf("writing event: %w", err)
			}
		}

		var text string
		var finishReason string
		if len(gr2.Candidates) > 0 {
			cand := gr2.Candidates[0]
			if len(cand.Content.Parts) > 0 {
				text = cand.Content.Parts[0].Text
			}
			if cand.FinishReason != "" {
				finishReason = geminiFinishReason(cand.FinishReason)
			}
		}

		chunk := model.ChatStreamChunk{
			ID:      genID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   req.Model,
			Choices: []model.StreamChoice{
				{
					Index:        0,
					Delta:        model.Delta{Content: text},
					FinishReason: finishReason,
				},
			},
		}
		if err := sse.WriteJSON(sw, chunk); err != nil {
			return &usage, fmt.Errorf("writing event: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return &usage, fmt.Errorf("reading stream: %w", err)
	}

	// Gemini has no [DONE] marker — signal done after stream ends.
	if err := sw.Done(); err != nil {
		return &usage, fmt.Errorf("writing done: %w", err)
	}

	return &usage, nil
}
